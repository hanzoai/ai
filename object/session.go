// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2023 The OpenAgent Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package object

import (
	"fmt"

	"github.com/hanzoai/ai/util"
	"github.com/hanzoai/dbx"
)

type Session struct {
	Owner       string     `db:"pk" json:"owner"`
	Name        string     `db:"pk" json:"name"`
	CreatedTime string     `json:"createdTime"`
	SessionId   StringList `json:"sessionId"`
}

func GetSessions(owner string) ([]*Session, error) {
	sessions := []*Session{}
	var err error
	if owner != "" {
		err = findAll(adapter.db, "session", &sessions, dbx.HashExp{"owner": owner}, "created_time DESC")
	} else {
		err = findAll(adapter.db, "session", &sessions, nil, "created_time DESC")
	}
	if err != nil {
		return sessions, err
	}
	return sessions, nil
}

func GetPaginationSessions(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Session, error) {
	return rowsPage[Session]("session", owner, offset, limit, field, value, sortField, sortOrder)
}

func GetSessionCount(owner, field, value string) (int64, error) {
	return rowCount("session", owner, field, value)
}

func GetSession(id string) (*Session, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	session := Session{Owner: owner, Name: name}
	get, err := getOne(adapter.db, "session", &session, pk2(session.Owner, session.Name))
	if err != nil {
		return &session, err
	}
	if !get {
		return nil, nil
	}
	return &session, nil
}

func UpdateSession(id string, session *Session) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	if ss, err := GetSession(id); err != nil {
		return false, err
	} else if ss == nil {
		return false, nil
	}
	session.Owner = owner
	session.Name = name
	err = adapter.db.Model(session).Update()
	if err != nil {
		return false, err
	}
	return true, nil
}

func removeExtraSessionIds(session *Session) {
	if len(session.SessionId) > 100 {
		session.SessionId = session.SessionId[(len(session.SessionId) - 100):]
	}
}

func AddSession(session *Session) (bool, error) {
	dbSession, err := GetSession(session.GetId())
	if err != nil {
		return false, err
	}
	if dbSession == nil {
		session.CreatedTime = util.GetCurrentTime()
		return addRow(session)
	} else {
		m := make(map[string]struct{})
		for _, v := range dbSession.SessionId {
			m[v] = struct{}{}
		}
		for _, v := range session.SessionId {
			if _, exists := m[v]; !exists {
				dbSession.SessionId = append(dbSession.SessionId, v)
			}
		}
		removeExtraSessionIds(dbSession)
		return UpdateSession(dbSession.GetId(), dbSession)
	}
}

func DeleteSession(id string) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	session, err := GetSession(id)
	if err != nil {
		return false, err
	}
	if session != nil {
	}
	affected, err := deleteByPK(adapter.db, "session", pk2(owner, name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func DeleteSessionId(id string, sessionId string) (bool, error) {
	session, err := GetSession(id)
	if err != nil {
		return false, err
	}
	if session == nil {
		return false, nil
	}
	session.SessionId = util.DeleteVal(session.SessionId, sessionId)
	if len(session.SessionId) == 0 {
		owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
		if err != nil {
			return false, err
		}
		affected, err := deleteByPK(adapter.db, "session", pk2(owner, name))
		if err != nil {
			return false, err
		}
		return affected != 0, nil
	} else {
		return UpdateSession(id, session)
	}
}

func (session *Session) GetId() string {
	return fmt.Sprintf("%s/%s", session.Owner, session.Name)
}

func IsSessionDuplicated(id string, sessionId string) (bool, error) {
	session, err := GetSession(id)
	if err != nil {
		return false, err
	}
	if session == nil {
		return false, nil
	} else {
		if len(session.SessionId) > 1 {
			return true, nil
		} else if len(session.SessionId) < 1 {
			return false, nil
		} else {
			return session.SessionId[0] != sessionId, nil
		}
	}
}
