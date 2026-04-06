// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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
package object
import (
	"strconv"
	"sync"
	"github.com/hanzoai/cloud/util"
	"github.com/hanzoai/cloud/util/guacamole"
	"github.com/hanzoai/dbx"
)
const (
	NoConnect    = "no_connect"
	Connecting   = "connecting"
	Connected    = "connected"
	Disconnected = "disconnected"
)
type Connection struct {
	Owner       string `db:"pk" json:"owner"`
	Name        string `db:"pk" json:"name"`
	CreatedTime string `json:"createdTime"`
	StartTime string `json:"startTime"`
	EndTime   string `json:"endTime"`
	Protocol      string `json:"protocol"`
	ConnectionId  string `json:"connectionId"`
	Node          string `json:"node"`
	Creator       string `json:"creator"`
	ClientIp      string `json:"clientIp"`
	UserAgent     string `json:"userAgent"`
	ClientIpDesc  string `json:"clientIpDesc"`
	UserAgentDesc string `json:"userAgentDesc"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	Status        string `json:"status"`
	Recording     string `json:"recording"`
	Code          int    `json:"code"`
	Message       string `json:"message"`
	Mode       string   `json:"mode"`
	Operations []string `db:"json varchar(1000)" json:"operations"`
	Reviewed     bool  `json:"reviewed"`
	CommandCount int64 `json:"commandCount"`
}
func (s *Connection) GetId() string {
	return util.GetIdFromOwnerAndName(s.Owner, s.Name)
}
func GetConnectionCount(owner, status, field, value string) (int64, error) {
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(session, "connection")
}
func GetConnections(owner string) ([]*Connection, error) {
	connections := []*Connection{}
	err := findAll(adapter.db, "connection", &connections, dbx.HashExp{"owner": owner}, "connected_time DESC")
	if err != nil {
		return connections, err
	}
	return connections, nil
}
func GetPaginationConnections(owner, status string, offset, limit int, field, value, sortField, sortOrder string) ([]*Connection, error) {
	connections := []*Connection{}
	q := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	if status != "" {
		q = q.AndWhere(dbx.HashExp{"status": status})
	}
	err := queryFind(q, "connection", &connections)
	if err != nil {
		return connections, err
	}
	return connections, nil
}
func GetSessionsByStatus(statuses []string) ([]*Connection, error) {
	connections := []*Connection{}
	err := adapter.db.Select().From("connection").Where(dbx.In("status", toInterfaceSlice(statuses)...)).All(&connections)
	if err != nil {
		return connections, err
	}
	return connections, nil
}
func getConnection(owner string, name string) (*Connection, error) {
	if owner == "" || name == "" {
		return nil, nil
	}
	connection := Connection{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "connection", &connection, pk2(connection.Owner, connection.Name))
	if err != nil {
		return &connection, err
	}
	if existed {
		return &connection, nil
	} else {
		return nil, nil
	}
}
func GetConnection(id string) (*Connection, error) {
	owner, name := util.GetOwnerAndNameFromIdNoCheck(id)
	return getConnection(owner, name)
}
func UpdateConnection(id string, connection *Connection, columns ...string) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	if oldConnection, err := getConnection(owner, name); err != nil {
		return false, err
	} else if oldConnection == nil {
		return false, nil
	}
	connection.Owner = owner
	connection.Name = name
	if len(columns) == 0 {
		err = adapter.db.Model(connection).Update()
		if err != nil {
			return false, err
		}
	} else {
		cols := dbx.Params{}
		for _, col := range columns {
			switch col {
			case "status":
				cols["status"] = connection.Status
			case "connected_time":
				cols["connected_time"] = connection.Status
			default:
				cols[col] = ""
			}
		}
		_, err = updateByPK(adapter.db, "connection", pk2(owner, name), cols)
		if err != nil {
			return false, err
		}
	}
	return true, nil
}
func DeleteConnection(connection *Connection) (bool, error) {
	affected, err := deleteByPK(adapter.db, "connection", pk2(connection.Owner, connection.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func DeleteConnectionById(id string) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	return DeleteConnection(&Connection{Owner: owner, Name: name})
}
func AddConnection(connection *Connection) (bool, error) {
	err := insertRow(adapter.db, connection)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func CreateConnection(connection *Connection, nodeId string, mode string) (*Connection, error) {
	node, err := GetNode(nodeId)
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, nil
	}
	connection.Owner = node.Owner
	connection.Name = util.GenerateId()
	connection.CreatedTime = util.GetCurrentTime()
	connection.Protocol = node.RemoteProtocol
	connection.Node = nodeId
	connection.Status = NoConnect
	connection.Mode = mode
	connection.Reviewed = false
	connection.Operations = []string{"paste", "copy", "createDir", "edit", "rename", "delete", "download", "upload", "fileSystem"}
	_, err = AddConnection(connection)
	if err != nil {
		return nil, err
	}
	respConnection := &Connection{
		Owner:      connection.Owner,
		Name:       connection.Name,
		Protocol:   node.RemoteProtocol,
		Operations: connection.Operations,
	}
	return respConnection, nil
}
func CloseDbSession(id string, code int, msg string) error {
	connection, err := GetConnection(id)
	if err != nil {
		return err
	}
	if connection == nil {
		return nil
	}
	if connection.Status == Disconnected {
		return nil
	}
	if connection.Status == Connecting {
		// The session has not been established successfully, so you do not need to save data
		_, err := DeleteConnection(connection)
		if err != nil {
			return err
		}
		return nil
	}
	connection.Status = Disconnected
	connection.Code = code
	connection.Message = msg
	connection.EndTime = util.GetCurrentTime()
	_, err = UpdateConnection(id, connection)
	if err != nil {
		return err
	}
	return nil
}
func WriteCloseMessage(guacSession *guacamole.Session, mode string, code int, msg string) {
	err := guacamole.NewInstruction("error", "", strconv.Itoa(code))
	_ = guacSession.WriteString(err.String())
	disconnect := guacamole.NewInstruction("disconnect")
	_ = guacSession.WriteString(disconnect.String())
}
var mutex sync.Mutex
func CloseConnection(id string, code int, msg string) error {
	mutex.Lock()
	defer mutex.Unlock()
	guacSession := guacamole.GlobalSessionManager.Get(id)
	if guacSession != nil {
		WriteCloseMessage(guacSession, guacSession.Mode, code, msg)
		if guacSession.Observer != nil {
			guacSession.Observer.Range(func(key string, ob *guacamole.Session) {
				WriteCloseMessage(ob, ob.Mode, code, msg)
			})
		}
	}
	guacamole.GlobalSessionManager.Delete(id)
	err := CloseDbSession(id, code, msg)
	if err != nil {
		return err
	}
	return nil
}
