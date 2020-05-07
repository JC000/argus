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

package store

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestToAttributes(t *testing.T) {
	assert := assert.New(t)

	// simple
	expected := Attributes{"1": "2", "stage": "beta"}
	actual := ToAttributes("1", "2", "stage", "beta")
	assert.Equal(expected, actual)

	// odd number
	actual = ToAttributes("1", "2", "stage", "beta", "rip")
	assert.Equal(expected, actual)

	// empty
	expected = Attributes{}
	actual = ToAttributes()
	assert.Equal(expected, actual)
}