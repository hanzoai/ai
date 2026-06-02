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
import {FileText, XCircle} from "lucide-react";

const ChatFileInput = ({
  files,
  onFileChange,
}) => {

  function handleRemoveFile(uid) {
    onFileChange(
      files.filter(file => file.uid !== uid)
    );
  }

  function renderFilePreview(uploadedFile) {
    const isImage = uploadedFile.file.type.startsWith("image/");
    if (isImage) {
      return (
        <div className="flex flex-col items-center gap-1">
          <img
            src={uploadedFile.content}
            className="w-[50px] h-[50px] object-cover rounded"
            alt={uploadedFile.file.name}
          />
          <div
            className="text-[10px] text-center max-w-[80px] overflow-hidden text-ellipsis whitespace-nowrap text-muted-foreground"
            title={uploadedFile.file.name}
          >
            {uploadedFile.file.name}
          </div>
        </div>
      );
    } else {
      return (
        <div className="flex flex-col items-center gap-1">
          <div className="w-[50px] h-[50px] rounded flex items-center justify-center bg-secondary">
            <FileText className="w-7 h-7 text-muted-foreground" />
          </div>
          <div
            className="text-[10px] text-center max-w-[80px] overflow-hidden text-ellipsis whitespace-nowrap text-muted-foreground"
            title={uploadedFile.file.name}
          >
            {uploadedFile.file.name}
          </div>
        </div>
      );
    }
  }

  return (
    <div className="flex gap-2.5 flex-wrap">
      {files.map((uploadedFile) => (
        <div
          key={uploadedFile.uid}
          className="relative"
        >
          {renderFilePreview(uploadedFile)}
          <button
            onClick={() => handleRemoveFile(uploadedFile.uid)}
            className="absolute -top-1.5 -right-1.5 text-destructive bg-background rounded-full cursor-pointer"
          >
            <XCircle className="w-4 h-4" />
          </button>
        </div>
      ))}
    </div>
  );
};

export default ChatFileInput;
