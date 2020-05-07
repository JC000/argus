/**
 * Copyright 2020 Comcast Cable Communications Management, LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 */

package cassandra

import (
	"context"
	"errors"
	"github.com/gocql/gocql"
	"github.com/goph/emperror"
	"github.com/xmidt-org/argus/store"
	"github.com/xmidt-org/webpa-common/logging"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/xmidt-org/themis/config"
	"go.uber.org/fx"
)

type CassandraIn struct {
	fx.In

	Unmarshaller config.Unmarshaller
}

type CassandraConfig struct {
	// Hosts to  connect to. Must have at least one
	Hosts []string

	// Database aka Keyspace for cassandra
	Database string

	// OpTimeout
	OpTimeout time.Duration

	// SSLRootCert used for enabling tls to the cluster. SSLKey, and SSLCert must also be set.
	SSLRootCert string
	// SSLKey used for enabling tls to the cluster. SSLRootCert, and SSLCert must also be set.
	SSLKey string
	// SSLCert used for enabling tls to the cluster. SSLRootCert, and SSLRootCert must also be set.
	SSLCert string
	// If you want to verify the hostname and server cert (like a wildcard for cass cluster) then you should turn this on
	// This option is basically the inverse of InSecureSkipVerify
	// See InSecureSkipVerify in http://golang.org/pkg/crypto/tls/ for more info
	EnableHostVerification bool

	// Username to authenticate into the cluster. Password must also be provided.
	Username string
	// Password to authenticate into the cluster. Username must also be provided.
	Password string

	// NumRetries for connecting to the db
	NumRetries int

	// WaitTimeMult the amount of time to wait before retrying to connect to the db
	WaitTimeMult time.Duration

	// MaxConnsPerHost max number of connections per host
	MaxConnsPerHost int
}

type CassandraClient struct {
	client   dbStore
	config   CassandraConfig
	logger   log.Logger
	measures Measures
}

func ProvideCassandra(in CassandraIn, metricsIn Measures, lc fx.Lifecycle, logger log.Logger) (store.S, error) {
	var config CassandraConfig
	err := in.Unmarshaller.UnmarshalKey("db", &config)
	if err != nil {
		return nil, err
	}
	client, err := CreateCassandraClient(config, metricsIn, logger)
	ticker := doEvery(time.Second*5, func(_ time.Time) {
		err := client.Ping()
		if err != nil {
			logging.Error(logger).Log(logging.MessageKey(), "ping failed", logging.ErrorKey(), err)
		}
	})
	if err != nil {
		return client, err
	}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			return nil
		},
		OnStop: func(context context.Context) error {
			ticker.Stop()
			client.Close()
			return nil
		},
	})
	return client, nil
}

func doEvery(d time.Duration, f func(time.Time)) *time.Ticker {
	ticker := time.NewTicker(d)
	go func() {
		for x := range ticker.C {
			f(x)
		}
	}()
	return ticker
}

func CreateCassandraClient(config CassandraConfig, measures Measures, logger log.Logger) (*CassandraClient, error) {
	if len(config.Hosts) == 0 {
		return nil, errors.New("number of hosts must be > 0")
	}

	validateConfig(&config)

	clusterConfig := gocql.NewCluster(config.Hosts...)
	clusterConfig.Consistency = gocql.LocalQuorum
	clusterConfig.Keyspace = config.Database
	clusterConfig.Timeout = config.OpTimeout
	// let retry package handle it
	clusterConfig.RetryPolicy = &gocql.SimpleRetryPolicy{NumRetries: 1}
	// setup ssl
	if config.SSLRootCert != "" && config.SSLCert != "" && config.SSLKey != "" {
		clusterConfig.SslOpts = &gocql.SslOptions{
			CertPath:               config.SSLCert,
			KeyPath:                config.SSLKey,
			CaPath:                 config.SSLRootCert,
			EnableHostVerification: config.EnableHostVerification,
		}
	}
	// setup authentication
	if config.Username != "" && config.Password != "" {
		clusterConfig.Authenticator = gocql.PasswordAuthenticator{
			Username: config.Username,
			Password: config.Password,
		}
	}

	session, err := connect(clusterConfig, logger)

	// retry if it fails
	waitTime := 1 * time.Second
	for attempt := 0; attempt < config.NumRetries && err != nil; attempt++ {
		time.Sleep(waitTime)
		session, err = connect(clusterConfig, logger)
		waitTime = waitTime * config.WaitTimeMult
	}
	if err != nil {
		return nil, emperror.WrapWith(err, "Connecting to database failed", "hosts", config.Hosts)
	}

	return &CassandraClient{
		client:   session,
		config:   config,
		logger:   logger,
		measures: measures,
	}, nil
}

func (s *CassandraClient) Push(key store.Key, item store.Item) error {
	err := s.client.Push(key, item)
	if err != nil {
		s.measures.SQLQueryFailureCount.With(store.TypeLabel, store.InsertType).Add(1.0)
		return err
	}
	s.measures.SQLQuerySuccessCount.With(store.TypeLabel, store.InsertType).Add(1.0)
	return nil
}

func (s *CassandraClient) Get(key store.Key) (store.Item, error) {
	item, err := s.client.Get(key)
	if err != nil {
		if err == noDataResponse {
			return item, store.KeyNotFoundError{Key: key}
		}
		s.measures.SQLQueryFailureCount.With(store.TypeLabel, store.ReadType).Add(1.0)
		return item, err
	}
	s.measures.SQLQuerySuccessCount.With(store.TypeLabel, store.ReadType).Add(1.0)
	return item, nil
}

func (s *CassandraClient) Delete(key store.Key) (store.Item, error) {
	item, err := s.client.Delete(key)
	if err != nil {
		if err == noDataResponse {
			return item, store.KeyNotFoundError{Key: key}
		}
		s.measures.SQLQueryFailureCount.With(store.TypeLabel, store.DeleteType).Add(1.0)
		return item, err
	}
	s.measures.SQLQuerySuccessCount.With(store.TypeLabel, store.DeleteType).Add(1.0)
	return item, err
}

func (s *CassandraClient) GetAll(bucket string) (map[string]store.Item, error) {
	item, err := s.client.GetAll(bucket)
	if err != nil {
		if err == noDataResponse {
			return item, store.KeyNotFoundError{Key: store.Key{
				Bucket: bucket,
			}}
		}
		s.measures.SQLQueryFailureCount.With(store.TypeLabel, store.ReadType).Add(1.0)
		return item, err
	}
	s.measures.SQLQuerySuccessCount.With(store.TypeLabel, store.ReadType).Add(1.0)
	return item, err
}

func (s *CassandraClient) Close() {
	s.client.Close()
}

// Ping is for pinging the database to verify that the connection is still good.
func (s *CassandraClient) Ping() error {
	err := s.client.Ping()
	if err != nil {
		s.measures.SQLQueryFailureCount.With(store.TypeLabel, store.PingType).Add(1.0)
		return emperror.WrapWith(err, "Pinging connection failed")
	}
	s.measures.SQLQuerySuccessCount.With(store.TypeLabel, store.PingType).Add(1.0)
	return nil
}

const (
	defaultOpTimeout             = time.Duration(10) * time.Second
	defaultDatabase              = "devices"
	defaultNumRetries            = 0
	defaultWaitTimeMult          = 1
	defaultMaxNumberConnsPerHost = 2
)

func validateConfig(config *CassandraConfig) {
	zeroDuration := time.Duration(0) * time.Second

	if config.OpTimeout == zeroDuration {
		config.OpTimeout = defaultOpTimeout
	}

	if config.Database == "" {
		config.Database = defaultDatabase
	}
	if config.NumRetries < 0 {
		config.NumRetries = defaultNumRetries
	}
	if config.WaitTimeMult < 1 {
		config.WaitTimeMult = defaultWaitTimeMult
	}
	if config.MaxConnsPerHost <= 0 {
		config.MaxConnsPerHost = defaultMaxNumberConnsPerHost
	}
}