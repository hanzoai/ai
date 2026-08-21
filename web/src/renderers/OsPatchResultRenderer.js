// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
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

import React from "react";
import {Alert, Space, Table, Tag, Typography} from "antd";
import i18next from "i18next";
import * as Setting from "../Setting";

const {Text} = Typography;

class OsPatchResultRenderer extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      patches: null,
      error: null,
      pageSize: 10,
    };
  }

  componentDidMount() {
    this.parseResult();
  }

  componentDidUpdate(prevProps) {
    if (prevProps.result !== this.props.result) {
      this.parseResult();
    }
  }

  getPatchId(patch) {
    // Generate a unique patch ID by combining KB and title
    // If KB exists, use it; otherwise use the title
    if (patch.kb) {
      return patch.kb;
    }
    // Use title as patchId when KB is not available
    return patch.title || "";
  }

  parseResult() {
    const {result} = this.props;
    if (!result) {
      this.setState({patches: null, error: null});
      return;
    }

    try {
      const parsed = JSON.parse(result);
      this.setState({patches: Array.isArray(parsed) ? parsed : [parsed], error: null});
    } catch (e) {
      this.setState({patches: null, error: "Failed to parse scan result: " + e.message});
    }
  }

  renderStatus(status) {
    const colorMap = {
      "Available": "blue",
      "Downloaded": "cyan",
      "Installed": "green",
      "Pending Restart": "orange",
      "Succeeded": "green",
      "Failed": "red",
    };
    return <Tag color={colorMap[status] || "default"}>{status}</Tag>;
  }

  renderBooleanTag(value, trueText, falseText) {
    if (value === true) {
      return <Tag color="orange">{trueText || "Yes"}</Tag>;
    } else if (value === false) {
      return <Tag color="green">{falseText || "No"}</Tag>;
    }
    return null;
  }

  render() {
    const {patches, error} = this.state;

    if (error) {
      return <Alert message={i18next.t("general:Error")} description={error} type="error" showIcon />;
    }

    if (!patches || patches.length === 0) {
      return <Alert message={i18next.t("scan:No patches found")} type="info" />;
    }

    const columns = [
      {
        title: i18next.t("general:Title"),
        dataIndex: "title",
        key: "title",
        width: "25%",
        ellipsis: true,
      },
      {
        title: i18next.t("scan:KB"),
        dataIndex: "kb",
        key: "kb",
        width: "8%",
        render: (kb) => kb ? <Text code>{kb}</Text> : null,
      },
      {
        title: i18next.t("general:Status"),
        dataIndex: "status",
        key: "status",
        width: "10%",
        render: (status) => this.renderStatus(status),
      },
      {
        title: i18next.t("general:Size"),
        dataIndex: "size",
        key: "size",
        width: "8%",
      },
      {
        title: i18next.t("scan:Reboot Required"),
        dataIndex: "rebootRequired",
        key: "rebootRequired",
        width: "10%",
        render: (value) => this.renderBooleanTag(value, i18next.t("general:Required"), i18next.t("scan:Not Required")),
      },
      {
        title: i18next.t("scan:Mandatory"),
        dataIndex: "isMandatory",
        key: "isMandatory",
        width: "8%",
        render: (value) => this.renderBooleanTag(value, i18next.t("scan:Yes"), i18next.t("scan:No")),
      },
      {
        title: i18next.t("scan:Installed On"),
        dataIndex: "installedOn",
        key: "installedOn",
        width: "13%",
        render: (date) => {
          if (!date) {return null;}
          try {
            const dateObj = new Date(date);
            return dateObj.toLocaleString();
          } catch (e) {
            return date;
          }
        },
      },
    ];

    return (
      <Space direction="vertical" size="middle" style={{width: "100%"}}>
        <Table
          columns={columns}
          dataSource={patches}
          pagination={{
            pageSize: this.state.pageSize,
            showSizeChanger: true,
            showTotal: (total) => `${i18next.t("general:Total")}: ${total}`,
            onChange: (page, pageSize) => {
              this.setState({pageSize});
            },
          }}
          size="small"
          rowKey={(record, index) => `patch-${index}-${this.getPatchId(record)}`}
          expandable={{
            expandedRowRender: (record) => (
              <div style={{margin: 0}}>
                <Text strong>{i18next.t("general:Description")}:</Text>
                <p style={{marginTop: "8px"}}>{record.description || i18next.t("scan:No description available")}</p>
                {record.categories && (
                  <p style={{marginTop: "8px"}}>
                    <Text strong>{i18next.t("scan:Categories")}:</Text> {record.categories}
                  </p>
                )}
              </div>
            ),
            rowExpandable: (record) => record.description || record.categories,
          }}
        />
      </Space>
    );
  }
}

export default OsPatchResultRenderer;
