// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2023 The OpenAgent Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package util

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ParseInt reads a whole number, and answers 0 for anything that is not one.
//
// It used to panic, and it is read from REQUEST INPUT at most of its call sites —
// pageSize, p, limit — so "?pageSize=abc" was a panic recovered into a 500 with a
// stack trace, on every paged listing in the module. A value that is not a number
// is not a page size.
//
// 0 is the right answer because 0 is what every caller here already handles:
// NewPaginator reads a size of zero or less as "use the default", and
// paginationOffset floors a negative offset at zero. Nothing downstream had to
// change to stop crashing.
func ParseInt(s string) int {
	i, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return i
}

func ParseIntWithError(s string) (int, error) {
	i, err := strconv.Atoi(s)
	if err != nil {
		return -1, err
	}

	if i < 0 {
		return -1, errors.New("negative version number")
	}

	return i, nil
}

func ParseFloat(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		panic(err)
	}

	return f
}

// GetOwnerAndNameFromIdNoCheck splits an id at its FIRST slash, so a name may
// itself contain slashes — which is what "no check" means here and why the
// checked form is not a drop-in for it.
//
// It used to index the second half unconditionally. An id carrying no slash
// yields one piece, so that read was out of range, and the id arrives on a query
// parameter: GET /v1/ai/get-file?id=x panicked, and the router turned it into a
// 500 with a stack trace where a malformed id deserves an answer. An id that
// names no name now names none, and the read below it finds nothing.
func GetOwnerAndNameFromIdNoCheck(id string) (string, string) {
	owner, name, _ := strings.Cut(id, "/")
	return owner, name
}

func GetOwnerAndNameFromIdWithError(id string) (string, string, error) {
	tokens := strings.Split(id, "/")
	if len(tokens) != 2 {
		return "", "", errors.New("id should be in the format of owner/name")
	}

	return tokens[0], tokens[1], nil
}

func GetIdFromOwnerAndName(owner string, name string) string {
	return fmt.Sprintf("%s/%s", owner, name)
}

func ReadStringFromPath(path string) string {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		panic(err)
	}

	return string(data)
}

func WriteStringToPath(s string, path string) {
	err := os.WriteFile(path, []byte(s), 0o644)
	if err != nil {
		panic(err)
	}
}

func WriteBytesToPath(b []byte, path string) error {
	return os.WriteFile(path, b, 0o644)
}

func DecodeBase64(s string) string {
	res, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}

	return string(res)
}

func GetRandomName() string {
	rand.Seed(time.Now().UnixNano())
	const charset = "0123456789abcdefghijklmnopqrstuvwxyz"
	result := make([]byte, 6)
	for i := range result {
		result[i] = charset[rand.Intn(len(charset))]
	}
	return string(result)
}

func GetId(owner, name string) string {
	if strings.Contains(name, "/") {
		return name
	}

	return fmt.Sprintf("%s/%s", owner, name)
}

// SnakeString transform XxYy to xx_yy
//
// The output names a SQL column (see the ORDER BY and LIKE call sites in
// object/), so it is schema, not cosmetics. Two rules are load-bearing and both
// have a test in string_test.go:
//
//   - Every capital gets its own separator, acronyms included: "OrgID" is
//     "org_i_d", not "org_id". That is how the live columns were named.
//   - A leading underscore already separates, so no second one is emitted:
//     "_Foo" is "_foo", not "__foo".
func SnakeString(s string) string {
	data := make([]byte, 0, len(s)*2)
	j := false
	num := len(s)
	for i := 0; i < num; i++ {
		d := s[i]
		if i > 0 && d >= 'A' && d <= 'Z' && j {
			data = append(data, '_')
		}
		if d != '_' {
			j = true
		}
		data = append(data, d)
	}
	result := strings.ToLower(string(data[:]))
	return strings.ReplaceAll(result, " ", "")
}

func GetChatFromProvider(owner, name string) string {
	return GetIdFromOwnerAndName(owner, fmt.Sprintf("chat_%s", name))
}

func GetRandomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[rand.Intn(len(charset))]
	}
	return string(result)
}
