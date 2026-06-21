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

import React from "react";
import * as VideoBackend from "../backend/VideoBackend";
import GridCards from "./GridCards";
import i18next from "i18next";

const PublicVideoListPage = (props) => {
  const [videos, setVideos] = React.useState(null);
  const [grade, setGrade] = React.useState("All");

  React.useEffect(() => {
    if (props.account === null) {
      return;
    }
    VideoBackend.getGlobalVideos()
      .then((res) => {
        let videos = res.data || [];
        videos = videos.filter((video) => video.isPublic === true);
        setVideos(videos);
      });
  }, [props.account]);

  const getItems = () => {
    if (videos === null) {
      return null;
    }

    let filteredVideos = videos;
    if (grade !== "All") {
      filteredVideos = videos.filter((video) => video.grade2 === grade);
    }

    return filteredVideos.map(video => {
      let comment = "";
      if (video.remarks2 && video.remarks2.length > 0) {
        comment = video.remarks2[0].text;
      }

      return {
        link: `/public-videos/${video.owner}/${video.name}`,
        name: video.displayName,
        description: video.description,
        logo: video.coverUrl,
        createdTime: comment,
      };
    });
  };

  const renderRadio = () => {
    const grades = [
      {value: "All", label: i18next.t("store:All")},
      {value: "Grade 1", label: i18next.t("video:Grade 1")},
      {value: "Grade 2", label: i18next.t("video:Grade 2")},
      {value: "Grade 3", label: i18next.t("video:Grade 3")},
    ];
    return (
      <div className="flex gap-2">
        {grades.map((g) => (
          <button
            key={g.value}
            onClick={() => setGrade(g.value)}
            className={`px-3 py-1.5 rounded text-xs ${grade === g.value ? "bg-zinc-200 text-black" : "bg-zinc-800 text-zinc-300 hover:bg-zinc-700"}`}
          >
            {g.label}
          </button>
        ))}
      </div>
    );
  };

  return (
    <div style={{display: "flex", justifyContent: "center", flexDirection: "column", alignItems: "center"}}>
      {
        renderRadio()
      }
      <GridCards items={getItems()} />
    </div>
  );
};

export default PublicVideoListPage;
