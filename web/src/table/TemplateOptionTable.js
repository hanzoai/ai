// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
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

import i18next from "i18next";
import React from "react";

class TemplateOptionTable extends React.Component {
  constructor(props) {
    super(props);
  }

  componentDidMount() {
    if (this.props.mode === "edit") {
      if (!this.props.templateOptions) {
        this.props.onUpdateTemplateOptions([]);
      }
    } else {
      if (!this.props.options) {
        this.props.onUpdateOptions(this.props.templateOptions.map(option => ({
          parameter: option.parameter,
          setting: option.default,
        })));
      }
    }
  }

  updateTemplateOptions(index, field, value) {
    const newOptions = this.props.templateOptions.map((option, i) => {
      if (i === index) {
        return {
          ...option,
          [field]: value,
        };
      }
      return option;
    });
    this.props.onUpdateTemplateOptions(newOptions);
  }

  updateOptions(index, field, value) {
    if (this.props.options === undefined) {
      return;
    }
    const newOptions = this.props.options;
    newOptions[index] = {
      ...newOptions[index],
      [field]: value,
    };

    this.props.onUpdateOptions(newOptions);
  }

  render() {
    const editOptionsColumn = [
      {
        title: i18next.t("application:Parameters"),
        dataIndex: "parameter",
        key: "parameter",
        width: "200px",
        render: (text, record, index) => (
          <Input value={text} onChange={e => this.updateTemplateOptions(index, "parameter", e.target.value)} />
        ),
      },
      {
        title: i18next.t("general:Type"),
        dataIndex: "type",
        key: "type",
        width: "200px",
        render: (text, record, index) => (
          <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={text}> {
              this.props.onUpdateTemplateOptions(
                this.props.templateOptions.map((option, i) => {
                  if (i === index) {
                    return {
                      ...option,
                      type: value,
                      options: value === "option" ? [String(record.default)] : null,
                    };
                  }
                  return option;
                })
              );
            }}
          />
        ),
      },
      {
        title: i18next.t("general:Required"),
        dataIndex: "required",
        key: "required",
        width: "100px",
        render: (text, record, index) => (
          <span className="px-2 py-0.5 rounded text-xs " + (text ? "bg-green-500/20 text-green-400" : "bg-zinc-800 text-zinc-500")">{text ? "ON" : "OFF"}</span>
        ),
      },
      {
        title: i18next.t("general:Default"),
        dataIndex: "default",
        key: "default",
        width: "200px",
        render: (text, record, index) => {
          if (record.type === "option") {
            return (
              <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={text}> ({
                  value: option,
                  label: option,
                }))}
                onChange={value => this.updateTemplateOptions(index, "default", value)}
              />
            );
          } else if (record.type === "number") {
            return (
              <Input
                type="number"
                value={text}
                onChange={e => this.updateTemplateOptions(index, "default", e.target.value.toString())}
              />
            );
          } else if (record.type === "boolean") {
            return (
              <span className="px-2 py-0.5 rounded text-xs " + (text === "true" ? "bg-green-500/20 text-green-400" : "bg-zinc-800 text-zinc-500")">{text === "true" ? "ON" : "OFF"}</span>
            );
          }
          return (
            <Input value={text} onChange={e => this.updateTemplateOptions(index, "default", e.target.value)} />
          );
        },
      },
      {
        title: i18next.t("general:Options"),
        dataIndex: "options",
        key: "options",
        width: "200px",
        render: (text, record, index) => {
          if (record.type === "option") {
            return (
              <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={Array.isArray(text) ? text : (text ? [text] : [])}> this.updateTemplateOptions(index, "options", value)}
              />
            );
          }
          return null;
        }
        ,
      },
      {
        title: i18next.t("general:Description"),
        dataIndex: "description",
        key: "description",
        width: "300px",
        render: (text, record, index) => (
          <Input value={text} onChange={e => this.updateTemplateOptions(index, "description", e.target.value)} />
        ),
      },
      {
        title: i18next.t("general:Action"),
        key: "action",
        render: (text, record, index) => (
          <button className="px-2 py-1 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200"> {
            const inputs = [...this.props.templateOptions];
            inputs.splice(index, 1);
            this.props.onUpdateTemplateOptions(inputs);
          }}>{i18next.t("general:Delete")}</button>
        ),
      },
    ];

    const optionsColumn = [
      {
        title: i18next.t("application:Parameters"),
        dataIndex: "parameter",
        key: "parameter",
        width: "200px",
        render: (text, record, index) => {
          if (text === "host") {
            text = i18next.t("application:Host") + "(host)";
          } else if (text === "tlsSecretName") {
            text = i18next.t("application:TLS secret name") + "(tlsSecretName)";
          }
          return (
            <span>
              {text}
              {record.required && <span style={{color: "red"}}>*</span>}
            </span>
          );
        },
      },
      {
        title: i18next.t("general:Setting"),
        dataIndex: "setting",
        key: "setting",
        render: (text, record, index) => {
          if (this.props.options?.[index]) {
            if (record.type === "option") {
              return (
                <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.props.options[index].setting}> {this.updateOptions(index, "setting", value);}}
                  options={record.options.map(option => ({
                    value: option,
                    label: option,
                  }))}
                />
              );
            } else if (record.type === "number") {
              return (
                <Input
                  type="number"
                  value={this.props.options[index].setting}
                  onChange={e => {this.updateOptions(index, "setting", e.target.value.toString());}}
                />
              );
            } else if (record.type === "boolean") {
              return (
                <span className="px-2 py-0.5 rounded text-xs " + (this.props.options[index].setting === "true" ? "bg-green-500/20 text-green-400" : "bg-zinc-800 text-zinc-500")">{this.props.options[index].setting === "true" ? "ON" : "OFF"}</span>
              );
            }
            return (
              <Input value={this.props.options[index].setting} onChange={e => {
                this.updateOptions(index, "setting", e.target.value);
              }} />
            );
          } else if (this.props.options) {
            this.updateOptions(index, "parameter", record.parameter);
            this.updateOptions(index, "setting", "");
          }
        },
      },
      {
        title: i18next.t("general:Description"),
        dataIndex: "description",
        key: "description",
        render: (text, record, index) => (
          <span>{text}</span>
        ),
      },
    ];

    return (
      <div style={{
        flexDirection: "row",
      }}>
        <div className="overflow-x-auto border border-zinc-800 rounded-lg"><table className="w-full text-sm text-left"><thead className="bg-zinc-900/80 border-b border-zinc-800"><tr>{this.props.mode === "edit" ? editOptionsColumn : optionsColumn.map(col => <th key={col.key || col.dataIndex} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}</tr></thead><tbody className="divide-y divide-zinc-800/50">{(this.props.templateOptions || []).map((record, index) => <tr key={typeof "index" === "function" ? ("index")(record) : record["index"] || index} className="hover:bg-zinc-900/50 transition-colors">{this.props.mode === "edit" ? editOptionsColumn : optionsColumn.map(col => <td key={col.key || col.dataIndex} className="px-3 py-2 text-zinc-300 whitespace-nowrap">{col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}</td>)}</tr>)}</tbody></table></div>
      </div>
    );
  }
}

export default TemplateOptionTable;
