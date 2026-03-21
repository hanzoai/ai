// Copyright 2023 Hanzo AI Inc. All Rights Reserved.
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
import * as VideoBackend from "./backend/VideoBackend";
import * as Setting from "./Setting";
import i18next from "i18next";
import Video from "./Video";
import LabelTable from "./table/LabelTable";
import * as Papa from "papaparse";
import VideoDataChart from "./VideoDataChart";
import WordCloudChart from "./WordCloudChart";
import ChatPage from "./ChatPage";
import TagTable from "./table/TagTable";
import * as TaskBackend from "./backend/TaskBackend";
import * as VideoConf from "./VideoConf";
import RemarkTable from "./table/RemarkTable";
import * as Conf from "./Conf";


class VideoEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      owner: props.match.params.owner,
      videoName: props.match.params.videoName,
      video: null,
      tasks: null,
      player: null,
      screen: null,
      videoObj: null,
      chatPageObj: null,
      videoData: null,
      segmentEditIndex: -1,
    };

    this.labelTable = React.createRef();
  }

  UNSAFE_componentWillMount() {
    this.getVideo();
    this.getTasks();
  }

  getVideo() {
    VideoBackend.getVideo(this.state.owner, this.state.videoName)
      .then((res) => {
        if (res.data === null) {
          this.props.history.push("/404");
          return;
        }

        if (res.status === "ok") {
          this.setState({
            video: res.data,
            currentTime: 0,
          });

          if (res.data?.dataUrl) {
            this.getDataAndParse(res.data.dataUrl);
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  getTasks() {
    TaskBackend.getTasks(this.props.account.name)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            tasks: res.data,
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  parseVideoField(key, value) {
    if ([""].includes(key)) {
      value = Setting.myParseInt(value);
    }
    return value;
  }

  updateVideoField(key, value) {
    value = this.parseVideoField(key, value);

    const video = this.state.video;
    video[key] = value;

    // if (key === "remarks") {
    //   if (value.filter((row) => (["Excellent", "Good"].includes(row.score))).length >= 2) {
    //     if (this.state.video.state === "In Review 1") {
    //       video.state = "In Review 2";
    //     }
    //   } else {
    //     if (this.state.video.state === "In Review 2") {
    //       video.state = "In Review 1";
    //     }
    //   }
    // }

    this.setState({
      video: video,
    });
  }

  onPause() {
    if (this.state.video.editMode === "Labeling" && this.state.video.tagOnPause && this.labelTable.current) {
      this.labelTable.current.addRow(this.state.video.labels);
    }
  }

  renderVideoContent() {
    if (this.state.video.videoId === "") {
      return null;
    }

    const task = {};
    task.video = {
      vid: this.state.video.videoId,
      playAuth: this.state.video.playAuth,
      cover: this.state.video.coverUrl,
      videoWidth: 1920,
      videoHeight: 1080,
      width: "100%",
      autoplay: false,
      isLive: false,
      rePlay: false,
      playsinline: true,
      preload: true,
      controlBarVisibility: "hover",
      useH5Prism: true,
    };

    return (
      <div style={{marginTop: "10px"}}>
        <div style={{fontSize: 16, marginTop: "10px", marginBottom: "10px"}}>
          {Setting.getLabel(i18next.t("video:Current time (second)"), i18next.t("video:Current time (second) - Tooltip"))} : {" "}
          <span className="px-2 py-0.5 bg-zinc-800 text-zinc-400 rounded text-xs">
            {this.state.currentTime}
          </span>
        </div>
        <div className="screen" style={{position: "absolute", zIndex: 100, pointerEvents: "none", width: "440px", height: "472px", marginLeft: "200px", marginRight: "200px", backgroundColor: "rgba(255,0,0,0)"}}></div>
        <Video task={task} labels={this.state.video.labels}
          onUpdateTime={(time) => {this.setState({currentTime: time});}}
          onCreatePlayer={(player) => {this.setState({player: player});}}
          onCreateScreen={(screen) => {this.setState({screen: screen});}}
          onCreateVideo={(videoObj) => {this.setState({videoObj: videoObj});}}
          onPause={() => {this.onPause();}}
        />
      </div>
    );
  }

  getDataAndParse(dataUrl) {
    fetch(dataUrl, {
      method: "GET",
    }).then(res => res.text())
      .then(res => {
        const result = Papa.parse(res, {header: true});
        let data = result.data;
        data = data.filter(item => item.time !== "");
        data = data.map(item => {
          const res = {};
          res.time = Number(item.time) - 5;
          res.data = Number(item.data);
          return res;
        });
        this.setState({
          videoData: data,
        });
      });
  }

  renderDataContent() {
    return (
      <React.Fragment>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Data"), i18next.t("general:Data - Tooltip"))} :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.video.dataUrl}> {
              this.getDataAndParse(value);
              this.updateVideoField("dataUrl", value);
            })}>
              {
                this.state.video.dataUrls?.map((dataUrl, index) => <option key={index} value={dataUrl}>{dataUrl.split("/").pop()}</option>)
              }
            </select>
          </div>
        </div>
        {
          this.state.videoData === null ? null : (
            <React.Fragment>
              <VideoDataChart key={"VideoDataChart1"} data={this.state.videoData} currentTime={this.state.currentTime} height={"100px"} />
              <VideoDataChart key={"VideoDataChart2"} data={this.state.videoData} currentTime={this.state.currentTime} interval={25} />
            </React.Fragment>
          )
        }
      </React.Fragment>
    );
  }

  isSegmentActive(segment) {
    return this.state.currentTime >= segment.startTime && this.state.currentTime < segment.endTime;
  }

  isSegmentsDisabled() {
    if (this.state.video.segments === null || this.state.video.segments.length === 0) {
      return true;
    }
    return false;
  }

  renderSegments() {
    if (this.isSegmentsDisabled()) {
      return null;
    }

    return (
      <div style={{marginTop: "20px", marginBottom: "20px"}}>
        <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
          <Timeline style={{marginTop: "10px", marginLeft: "10px"}}
            items={
              this.state.video.segments.map((segment, index) => {
                return (
                  {
                    color: this.isSegmentActive(segment) ? "blue" : "gray",
                    dot: this.isSegmentActive(segment) ?  : null,
                    children: (
                      <div style={{marginTop: "-10px", cursor: "pointer"}} onClick={() => {
                        this.setState({
                          currentTime: segment.startTime,
                        });

                        if (this.state.videoObj) {
                          this.state.videoObj.changeTime(segment.startTime);
                        }
                      }}>
                        <div style={{display: "inline-block", width: "75px", fontWeight: this.isSegmentActive(segment) ? "bold" : "normal"}}>{Setting.getTimeFromSeconds(segment.startTime)}</div>
                        &nbsp;&nbsp;
                        {
                          Setting.getSpeakerTag(segment.speaker)
                        }
                        <span className="px-2 py-0.5 bg-zinc-800 text-zinc-400 rounded text-xs">
                          {
                            (this.state.segmentEditIndex !== index) ? segment.text : (
                              <Input style={{width: "400px"}} value={segment.text} onChange={e => {
                                const segments = this.state.video.segments;
                                segments[index].text = e.target.value;

                                this.updateVideoField("segments", segments);
                              }} />
                            )
                          }
                        </span>
                        {
                          (this.state.segmentEditIndex !== index) ? (
                            <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700">} size="small" onClick={(event) => {
                              event.stopPropagation();
                              this.setState({
                                segmentEditIndex: index,
                              });
                            }} />
                          ) : (
                            <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700">} size="small" onClick={(event) => {
                              event.stopPropagation();
                              this.setState({
                                segmentEditIndex: -1,
                              });
                            }} />
                          )
                        }
                      </div>
                    ),
                  }
                );
              })
            }
          />
        </div>
      </div>
    );
  }

  renderSegmentTags() {
    return (
      <div>
        <span className="px-2 py-0.5 bg-zinc-800 text-zinc-400 rounded text-xs"> {this.updateVideoField("segments", value);}}
          onUpdateVideoField={(key, value) => {this.updateVideoField(key, value);}}
        />
      </div>
    );
  }

  isWordsDisabled() {
    if (this.state.video.wordCountMap === null || this.state.video.wordCountMap.length === 0) {
      return true;
    }
    return false;
  }

  renderWords() {
    if (this.isWordsDisabled()) {
      return null;
    }

    return (
      <WordCloudChart wordCountMap={this.state.video.wordCountMap} />
    );
  }

  generatePlan() {
    let text = this.state.video.template;
    text = text.replaceAll("${stage}", this.state.video.stage);
    text = text.replaceAll("${grade}", this.state.video.grade);
    text = text.replaceAll("${subject}", this.state.video.subject);
    text = text.replaceAll("${topic}", this.state.video.topic);
    text = text.replaceAll("${keywords}", this.state.video.keywords);
    // Setting.showMessage("success", text);
    this.state.chatPageObj.sendMessage(text, "", true);
  }

  renderAiAssistantOptions() {
    return (
      <React.Fragment>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("video:School"), i18next.t("video:School - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.video.school} onChange={e => {
              this.updateVideoField("school", e.target.value);
            }} />
          </div>
          <div className="flex-1">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("video:Stage"), i18next.t("video:Stage - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.video.stage} onChange={e => {
              this.updateVideoField("stage", e.target.value);
            }} />
          </div>
          <div className="flex-1">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("video:Grade"), i18next.t("video:Grade - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.video.grade} onChange={e => {
              this.updateVideoField("grade", e.target.value);
            }} />
          </div>
          <div className="flex-1">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("video:Class"), i18next.t("video:Class - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.video.class} onChange={e => {
              this.updateVideoField("class", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("video:Keywords"), i18next.t("video:Keywords - Tooltip"))} :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.video.keywords}> {this.updateVideoField("keywords", value);})}>
              {
                this.state.video.keywords?.map((item, index) => <option key={index} value={item}>{item}</option>)
              }
            </select>
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("store:Subject"), i18next.t("store:Subject - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.video.subject} onChange={e => {
              this.updateVideoField("subject", e.target.value);
            }} />
          </div>
          <div className="flex-1">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("video:Topic"), i18next.t("video:Topic - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.video.topic} onChange={e => {
              this.updateVideoField("topic", e.target.value);
            }} />
          </div>
          <div className="flex-1">
          <div className="flex-1">
            <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.generatePlan()}>{i18next.t("video:Generate Plan")}</button>
          </div>
          <div className="flex-1">
          <div className="flex-1">
             {
                this.updateVideoField("template", e.target.value);
              }} />
            }>
              <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}>{i18next.t("template:Edit Template")}</button>
            
          </div>
        </div>
      </React.Fragment>
    );
  }

  renderChat() {
    return (
      <div style={{marginTop: "20px"}}>
        {
          this.renderAiAssistantOptions()
        }
        <div style={{marginTop: "20px"}}>
          <ChatPage onCreateChatPage={(chatPageObj) => {this.setState({chatPageObj: chatPageObj});}} account={this.props.account} />
        </div>
      </div>
    );
  }

  renderLabels() {
    return (
      <div style={{marginTop: "20px"}}>
        <LabelTable
          ref={this.labelTable}
          title={i18next.t("task:Labels")}
          account={this.props.account}
          table={this.state.video.labels}
          currentTime={this.state.currentTime}
          video={this.state.video}
          player={this.state.player}
          screen={this.state.screen}
          videoObj={this.state.videoObj}
          onUpdateTable={(value) => {this.updateVideoField("labels", value);}}
          onUpdateTagOnPause={(value) => {this.updateVideoField("tagOnPause", value);}}
        />
      </div>
    );
  }

  requireUserOrAdmin(video) {
    if (!this.requireAdmin()) {
      return false;
    } else if (this.props.account.type !== "video-normal-user") {
      return true;
    }

    if (!video) {
      return false;
    } else {
      return video.remarks && video.remarks.length > 0 || video.remarks2 && video.remarks2.length > 0 || video.state !== "Draft";
    }
  }

  requireUserOrReviewerOrAdmin(video) {
    if (this.props.account.type === "video-reviewer1-user" || this.props.account.type === "video-reviewer2-user") {
      return false;
    } else {
      return this.requireUserOrAdmin(video);
    }
  }

  requireReviewerOrAdmin() {
    return !(!this.requireAdmin() || this.props.account.type === "video-reviewer1-user" || this.props.account.type === "video-reviewer2-user");
  }

  requireReviewer1OrAdmin() {
    return !(!this.requireAdmin() || this.props.account.type === "video-reviewer1-user");
  }

  requireReviewer2OrAdmin() {
    return !(!this.requireAdmin() || this.props.account.type === "video-reviewer2-user");
  }

  requireReviewer2OrAdminOrPublic() {
    if (this.props.account.type === "video-normal-user" && this.state.video?.state === "Published") {
      return false;
    }

    return !(!this.requireAdmin() || this.props.account.type === "video-reviewer2-user");
  }

  requireAdmin() {
    if (Setting.isAdminUser(this.props.account)) {
      return false;
    }

    return !(this.props.account.type === "video-admin-user");
  }

  renderVideo() {
    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
          {i18next.t("video:Edit Video")}&nbsp;&nbsp;&nbsp;&nbsp;
          {
            this.requireUserOrReviewerOrAdmin(this.state.video) ? (
              <>
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.exit()}>{i18next.t("general:Exit")}</button>
              </>
            ) : (
              <>
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitVideoEdit(false)}>{i18next.t("general:Save")}</button>
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitVideoEdit(true)}>{i18next.t("general:Save & Exit")}</button>
              </>
            )
          }
        </div>
      } style={{marginLeft: "5px"}} type="inner">
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Name"), i18next.t("general:Name - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input disabled={this.requireUserOrAdmin(this.state.video)} value={this.state.video.name} onChange={e => {
              this.updateVideoField("name", e.target.value);
            }} />
          </div>
          <div className="flex-1">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Display name"), i18next.t("general:Display name - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input disabled={this.requireUserOrAdmin(this.state.video)} value={this.state.video.displayName} onChange={e => {
              this.updateVideoField("displayName", e.target.value);
            }} />
          </div>
          <div className="flex-1">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("video:Video ID"), i18next.t("video:Video ID - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input disabled={this.requireUserOrAdmin(this.state.video)} value={this.state.video.videoId} onChange={e => {
              this.updateVideoField("videoId", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Description"), i18next.t("general:Description - Tooltip"))} :
          </div>
          <div className="flex-1">
            <span className="text-zinc-300 text-sm"> {
              this.updateVideoField("description", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("video:Grade"), i18next.t("video:Grade - Tooltip"))} :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.video.grade} disabled> {
              this.updateVideoField("grade", value);
              this.updateVideoField("grade2", VideoConf.getGrade2(value));
              this.updateVideoField("unit", "");
              this.updateVideoField("lesson", "");
            })}>
              {
                VideoConf.GradeOptions
                // .sort((a, b) => a.name.localeCompare(b.name))
                  .map((item, index) => <option key={index} value={item.id}>{item.name}</option>)
              }
            </select>
          </div>
          <div className="flex-1">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Unit"), i18next.t("general:Unit - Tooltip"))} :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.video.unit} disabled> {
              this.updateVideoField("unit", value);
              this.updateVideoField("lesson", "");
            })}>
              {
                VideoConf.getUnitOptions(this.state.video.grade)
                // .sort((a, b) => a.name.localeCompare(b.name))
                  .map((item, index) => <option key={index} value={item.id}>{item.id}</option>)
              }
            </select>
          </div>
          <div className="flex-1">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("video:Lesson"), i18next.t("video:Lesson - Tooltip"))} :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.video.lesson} disabled> {this.updateVideoField("lesson", value);})}>
              {
                VideoConf.getLessonOptions(this.state.video.grade, this.state.video.unit)
                // .sort((a, b) => a.name.localeCompare(b.name))
                  .map((item, index) => <option key={index} value={item.id}>{`${item.id} (${item.name})`}</option>)
              }
            </select>
          </div>
        </div>
        {
          this.requireReviewerOrAdmin() ? null : (
            <div className="flex flex-col sm:flex-row gap-2 mt-4">
              <div className="flex-1">
                {Setting.getLabel(i18next.t("video:Remarks1"), i18next.t("video:Remarks1 - Tooltip"))} :
              </div>
              <div className="flex-1">
                <RemarkTable
                  title={i18next.t("video:Remarks1")}
                  account={this.props.account}
                  maxRowCount={-1}
                  disabled={this.requireReviewer1OrAdmin(this.state.video)}
                  table={this.state.video.remarks}
                  onUpdateTable={(value) => {
                    this.updateVideoField("remarks", value);
                    if (value.length > 0 && this.state.video.state === "Draft") {
                      this.updateVideoField("state", "In Review 1");
                    } else if (value.length === 0 && this.state.video.remarks2.length === 0 && this.state.video.state.startsWith("In Review")) {
                      this.updateVideoField("state", "Draft");
                    }
                  }}
                />
              </div>
            </div>
          )
        }
        {
          this.requireReviewer2OrAdminOrPublic() ? null : (
            <div className="flex flex-col sm:flex-row gap-2 mt-4">
              <div className="flex-1">
                {Setting.getLabel(i18next.t("video:Remarks2"), i18next.t("video:Remarks2 - Tooltip"))} :
              </div>
              <div className="flex-1">
                <RemarkTable
                  title={i18next.t("video:Remarks2")}
                  account={this.props.account}
                  maxRowCount={1}
                  disabled={this.requireReviewer2OrAdmin(this.state.video)}
                  table={this.state.video.remarks2}
                  onUpdateTable={(value) => {
                    this.updateVideoField("remarks2", value);
                    if (value.length > 0 && this.state.video.state === "Draft") {
                      this.updateVideoField("state", "In Review 2");
                    } else if (value.length === 0 && this.state.video.remarks.length === 0 && this.state.video.state.startsWith("In Review")) {
                      this.updateVideoField("state", "Draft");
                    }

                    if (this.state.video.remarks2.filter((row) => (row.user === this.props.account.name)).length > 0) {
                      const isPublic = this.state.video.remarks2.filter((row) => (row.user === this.props.account.name))[0].isPublic;
                      this.updateVideoField("isPublic", isPublic);
                    } else {
                      this.updateVideoField("isPublic", false);
                    }
                  }}
                />
              </div>
            </div>
          )
        }
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:State"), i18next.t("general:State - Tooltip"))} :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.video.state} disabled> {
              this.updateVideoField("state", value);
            })}>
              {
                [
                  {id: "Draft", name: i18next.t("video:Draft")},
                  {id: "In Review 1", name: i18next.t("video:In Review 1")},
                  {id: "In Review 2", name: i18next.t("video:In Review 2")},
                  {id: "Published", name: i18next.t("video:Published")},
                ].map((item, index) => <option key={index} value={item.id}>{item.name}</option>)
              }
            </select>
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("video:Is public"), i18next.t("video:Is public - Tooltip"))} :
          </div>
          <div className="flex-1">
            <span className="px-2 py-0.5 rounded text-xs " + (this.state.video.isPublic ? "bg-green-500/20 text-green-400" : "bg-zinc-800 text-zinc-500")">{this.state.video.isPublic ? "ON" : "OFF"}</span>
          </div>
        </div>
        {
          this.requireAdmin() ? null : (
            <div className="flex flex-col sm:flex-row gap-2 mt-4">
              <div className="flex-1">
                {Setting.getLabel(i18next.t("general:Download"), i18next.t("general:Download"))} :
              </div>
              <div className="flex-1">
                <a target="_blank" rel="noreferrer" href={this.state.video.downloadUrl}>
                  <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700">} style={{marginRight: "10px"}} type="primary">{i18next.t("general:Download")}</button>
                </a>
              </div>
            </div>
          )
        }
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("video:Cover"), i18next.t("video:Cover - Tooltip"))} :
          </div>
          <div className="flex-1">
            <div className="flex flex-col sm:flex-row gap-2 mt-4">
              <div className="flex-1">
                {Setting.getLabel(i18next.t("general:URL"), i18next.t("general:URL - Tooltip"))} :
              </div>
              <div className="flex-1">
                <input className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 disabled:opacity-50" disabled />} value={this.state.video.coverUrl} onChange={e => {
                  this.updateVideoField("coverUrl", e.target.value);
                }} />
              </div>
            </div>
            <div className="flex flex-col sm:flex-row gap-2 mt-4">
              <div className="flex-1">
                {Setting.getLabel(i18next.t("general:Preview"), i18next.t("general:Preview - Tooltip"))} :
              </div>
              <div className="flex-1">
                <a target="_blank" rel="noreferrer" href={this.state.video.coverUrl}>
                  <img src={this.state.video.coverUrl} alt={this.state.video.coverUrl} height={90} style={{marginBottom: "20px"}} />
                </a>
              </div>
            </div>
          </div>
        </div>
        {
          this.props.account.type.startsWith("video-") ? null : (
            <Segmented
              options={[
                {
                  label: (
                    <div style={{padding: 4}}>
                      <Avatar src={`${Conf.StaticBaseUrl}/img/email_mailtrap.png`} />
                            &nbsp;
                      <span style={{fontWeight: "bold"}}>Labeling</span>
                    </div>
                  ),
                  value: "Labeling",
                },
                {
                  label: (
                    <div style={{padding: 4}}>
                      <Avatar src={`${Conf.StaticBaseUrl}/img/social_slack.png`} />
                            &nbsp;
                      <span style={{fontWeight: "bold"}}>Text Recognition</span>
                    </div>
                  ),
                  value: "Text Recognition",
                  disabled: this.isSegmentsDisabled(),
                },
                {
                  label: (
                    <div style={{padding: 4}}>
                      <Avatar src={`${Conf.StaticBaseUrl}/img/social_yandex.png`} />
                            &nbsp;
                      <span style={{fontWeight: "bold"}}>Text Tagging</span>
                    </div>
                  ),
                  value: "Text Tagging",
                  disabled: this.isSegmentsDisabled(),
                },
                {
                  label: (
                    <div style={{padding: 4}}>
                      <Avatar src={`${Conf.StaticBaseUrl}/img/social_cloudflare.png`} />
                            &nbsp;
                      <span style={{fontWeight: "bold"}}>Word Cloud</span>
                    </div>
                  ),
                  value: "Word Cloud",
                  disabled: this.isWordsDisabled(),
                },
                {
                  label: (
                    <div style={{padding: 4}}>
                      <Avatar src={`${Conf.StaticBaseUrl}/img/social_openai.svg`} />
                            &nbsp;
                      <span style={{fontWeight: "bold"}}>AI Assistant</span>
                    </div>
                  ),
                  value: "AI Assistant",
                  disabled: this.props.account.type.startsWith("video-"),
                },
              ]}
              block value={this.state.video.editMode} onChange={checked => {
                this.updateVideoField("editMode", checked);
              }}
            />
          )
        }
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          {
            (this.state.video.editMode === "Text Tagging" || this.state.video.editMode === "AI Assistant") ? null : (
              <React.Fragment>
                <div className="flex-1">
                  {Setting.getLabel(i18next.t("video:Video"), i18next.t("video:Video - Tooltip"))} :
                </div>
                <div className="flex-1">
                  <React.Fragment>
                    <Affix offsetTop={50}>
                      {
                        this.state.video !== null ? this.renderVideoContent() : null
                      }
                      {
                        this.state.video.dataUrl !== "" ? this.renderDataContent() : null
                      }
                    </Affix>
                  </React.Fragment>
                </div>
                <div className="flex-1">
                </div>
              </React.Fragment>
            )
          }
          <div className="flex-1">
            {
              this.state.video.editMode === "Labeling" ? this.renderLabels() :
                this.state.video.editMode === "Text Recognition" ? this.renderSegments() :
                  this.state.video.editMode === "Text Tagging" ? this.renderSegmentTags() :
                    this.state.video.editMode === "Word Cloud" ? this.renderWords() :
                      this.renderChat()
            }
          </div>
        </div>
      </div>
    );
  }

  exit() {
    this.props.history.push("/videos");
  }

  submitVideoEdit(exitAfterSave) {
    if ((this.state.video.remarks.filter((row) => (row.user === this.props.account.name && ["Excellent", "Good"].includes(row.score))).length > 0) ||
        (this.state.video.remarks2.filter((row) => (row.user === this.props.account.name && ["Excellent", "Good"].includes(row.score))).length > 0) ||
        this.props.account.type === "video-normal-user") {
      if (this.state.video.labels.filter((row) => (row.user === this.props.account.name)).length === 0) {
        Setting.showMessage("error", i18next.t("video:Please add a new label first before saving!"));
        return;
      }
    }

    const video = Setting.deepCopy(this.state.video);
    VideoBackend.updateVideo(this.state.video.owner, this.state.videoName, video)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data) {
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({
              videoName: this.state.video.name,
            });
            if (exitAfterSave) {
              this.props.history.push("/videos");
            } else {
              this.props.history.push(`/videos/${this.state.video.owner}/${this.state.video.name}`);
            }
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to save"));
            this.updateVideoField("name", this.state.videoName);
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${error}`);
      });
  }

  render() {
    return (
      <div>
        {
          this.state.video !== null ? this.renderVideo() : null
        }
        <div style={{marginTop: "20px", marginLeft: "40px"}}>
          {
            this.requireUserOrReviewerOrAdmin(this.state.video) ? (
              <>
                <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.exit()}>{i18next.t("general:Exit")}</button>
              </>
            ) : (
              <>
                <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitVideoEdit(false)}>{i18next.t("general:Save")}</button>
                <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitVideoEdit(true)}>{i18next.t("general:Save & Exit")}</button>
              </>
            )
          }
        </div>
      </div>
    );
  }
}

export default VideoEditPage;
