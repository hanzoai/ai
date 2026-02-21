// Copyright 2026 The Casibase Authors. All Rights Reserved.
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

import "github.com/casibase/casibase/model"

// GetAnswerFake returns a hardcoded string without calling the LLM API, for fast local debugging.
// Switch between GetAnswer and GetAnswerFake by commenting one of the two call sites (e.g. in task_analyze.go).
// Replace fakeAnswer below with your real API response (multi-line raw string is fine).
func GetAnswerFake(provider string, question string, lang string) (string, *model.ModelResult, error) {
	const fakeAnswer = `{
  "title": "项目实验班整体教学与课程实施方案",
  "designer": "北京师范大学第三附属中学项目实验班团队",
  "stage": "高中",
  "participants": "项目实验班全体学生、学科教师、外聘专家",
  "grade": "高一至高二",
  "instructor": "白计明（总负责人）、杨宝华（执行组长）",
  "subject": "数学",
  "school": "北京师范大学第三附属中学",
  "otherSubjects": "物理,化学,地理,语文,现代传媒,科技创新",
  "textbook": "国家高中必修课程教材（版本未明确）",
  "score": 82.2,
  "categories": [
    {
      "name": "1.课时的教学目标、教学重点和难点",
      "score": 82.8,
      "items": [
        {
          "name": "1.1.1契合课标，体现学科核心素养，覆盖知识、能力与情感三维目标",
          "score": 89,
          "advantage": "文本整体目标强调自主学习能力、思维能力和人格发展，与课程改革和核心素养理念在宏观层面一致。",
          "disadvantage": "未针对高中数学具体课标条目展开，没有明确数学核心素养在单一课时或单元中的落实方式。",
          "suggestion": "需明确对照《普通高中数学课程标准》，在具体课时中细化知识、能力与情感态度价值观目标。"
        },
        {
          "name": "1.1.2精准具体，表述明确可操作",
          "score": 73,
          "advantage": "提出了阅读量、研究性学习等可操作性要求。",
          "disadvantage": "教学目标停留在项目层面，缺乏具体课时可直接执行和评价的数学学习目标。",
          "suggestion": "补充以“学生能够……”“通过……解决……”表述的具体数学学习目标。"
        },
        {
          "name": "1.1.3分层递进，呈现认知梯度",
          "score": 78,
          "advantage": "在年级推进上体现了从讲座到小课题再到深入研究的递进关系。",
          "disadvantage": "未体现数学知识与能力层面的认知递进，缺乏由浅入深的数学学习路径。",
          "suggestion": "在数学学科内构建由基础理解—方法掌握—综合应用的分层目标体系。"
        },
        {
          "name": "1.2.1核心内容突出，聚焦课程核心概念或关键技能",
          "score": 83,
          "advantage": "强调必修课程与拓展课程结合，重视方法与思维。",
          "disadvantage": "没有指出任何具体数学核心概念或关键技能。",
          "suggestion": "明确如函数、几何、概率统计等核心内容在教学中的定位。"
        },
        {
          "name": "1.2.2课标对应明确，直接关联课程标准能力要求",
          "score": 74,
          "advantage": "总体方向与国家课程要求一致。",
          "disadvantage": "缺乏课标条款级别的对应说明。",
          "suggestion": "在教学设计中标注对应的课标条款与能力要求。"
        },
        {
          "name": "1.2.3基于学情定位学生需要着重理解的内容",
          "score": 94,
          "advantage": "针对项目实验班学生的高起点特征进行了整体定位。",
          "disadvantage": "未基于具体数学学习困难分析重点内容。",
          "suggestion": "结合学生数学认知特点，明确易错点和理解难点。"
        },
        {
          "name": "1.3.1确保难易程度适配学情",
          "score": 84,
          "advantage": "项目实验班定位决定了较高难度和挑战性。",
          "disadvantage": "缺乏具体教学内容，难度适配无法判断。",
          "suggestion": "在具体数学教学中标注基础、提升与挑战性任务。"
        },
        {
          "name": "1.3.2明确可突破路径或策略",
          "score": 79,
          "advantage": "提到探究式、小课题研究等方式。",
          "disadvantage": "没有针对数学难点给出解决策略。",
          "suggestion": "针对典型数学难点设计模型化、图示化或探究路径。"
        },
        {
          "name": "1.3.3具有针对性和挑战性",
          "score": 91,
          "advantage": "整体培养目标具有较强挑战性。",
          "disadvantage": "未落实到数学课堂的具体问题。",
          "suggestion": "在数学拓展任务中引入竞赛型或真实问题。"
        }
      ]
    },
    {
      "name": "2.课时的教学过程",
      "score": 74.4,
      "items": [
        {
          "name": "2.1.1清晰区分教学过程的不同阶段",
          "score": 68,
          "advantage": "对年级阶段和课程类型有宏观划分。",
          "disadvantage": "不存在具体课时教学阶段设计。",
          "suggestion": "补充单节数学课的导入、探究、巩固与总结环节。"
        },
        {
          "name": "2.1.2各阶段之间逻辑连贯，确保教学节奏紧凑",
          "score": 69,
          "advantage": "整体课程规划逻辑较完整。",
          "disadvantage": "缺乏课堂层面的节奏设计。",
          "suggestion": "在课时设计中明确时间分配与过渡方式。"
        },
        {
          "name": "2.1.3每个阶段的教学目标与整体教学目标一致",
          "score": 76,
          "advantage": "宏观目标一致性较强。",
          "disadvantage": "缺乏课时目标，无法验证一致性。",
          "suggestion": "建立课时目标—单元目标—项目目标的对齐关系。"
        },
        {
          "name": "2.2.1设计有效提问或情景引入",
          "score": 63,
          "advantage": "倡导启发式教学。",
          "disadvantage": "无具体提问或引入示例。",
          "suggestion": "在数学课堂中设计问题链或真实情境问题。"
        },
        {
          "name": "2.2.2展示清晰的板书、多媒体或案例",
          "score": 64,
          "advantage": "具备外部资源条件。",
          "disadvantage": "未涉及任何课堂呈现方式。",
          "suggestion": "明确数学课堂板书结构和技术工具使用方式。"
        },
        {
          "name": "2.2.3引导学生参与互动",
          "score": 84,
          "advantage": "强调讨论、研究和交流。",
          "disadvantage": "互动方式未落实到课堂操作层面。",
          "suggestion": "设计小组讨论、展示与评价机制。"
        },
        {
          "name": "2.2.4设计针对性练习或任务",
          "score": 74,
          "advantage": "有研究性学习和任务意识。",
          "disadvantage": "缺乏数学练习设计。",
          "suggestion": "补充分层数学作业和探究任务。"
        },
        {
          "name": "2.3.1学生在课堂中扮演主动角色",
          "score": 96,
          "advantage": "项目制和导师制突出学生主体性。",
          "disadvantage": "未体现课堂层面如何操作。",
          "suggestion": "在数学课中引入学生主讲、展示和反思。"
        },
        {
          "name": "2.3.2学生积极回应教师提问",
          "score": 74,
          "advantage": "鼓励交流与讨论。",
          "disadvantage": "缺乏具体机制。",
          "suggestion": "结合提问评价与课堂反馈机制。"
        },
        {
          "name": "2.3.3鼓励学生提出多样化观点",
          "score": 89,
          "advantage": "研究性学习有利于多方案生成。",
          "disadvantage": "数学层面的多解问题未体现。",
          "suggestion": "设计一题多解与开放性问题。"
        },
        {
          "name": "2.4.1-2.4.3人工智能应用",
          "score": 61,
          "advantage": "暂无明显体现。",
          "disadvantage": "完全未涉及AI技术在教学中的应用。",
          "suggestion": "引入学习数据分析、智能作业推送与即时反馈。"
        }
      ]
    },
    {
      "name": "3.数学学科核心素养",
      "score": 89.3,
      "items": [
        {
          "name": "3.1数学抽象（整体）",
          "score": 94,
          "advantage": "阅读、研究活动有助于抽象能力发展。",
          "disadvantage": "未在数学教学中明确体现抽象过程。",
          "suggestion": "在教学中强调从实际问题到数学结构的抽象。"
        },
        {
          "name": "3.2逻辑推理（整体）",
          "score": 89,
          "advantage": "研究性学习有利于逻辑表达。",
          "disadvantage": "缺乏数学证明与推理训练设计。",
          "suggestion": "增加数学论证与推理环节。"
        },
        {
          "name": "3.3数学建模（整体）",
          "score": 99,
          "advantage": "小课题研究天然契合数学建模。",
          "disadvantage": "建模过程与评价标准未明确。",
          "suggestion": "明确建模步骤与成果要求。"
        },
        {
          "name": "3.4直观想象（整体）",
          "score": 84,
          "advantage": "跨学科可能涉及空间与图形。",
          "disadvantage": "未体现几何直观的培养。",
          "suggestion": "结合几何、函数图像强化直观想象。"
        },
        {
          "name": "3.5数学运算（整体）",
          "score": 79,
          "advantage": "必修课程保证基本训练。",
          "disadvantage": "未专门设计运算能力提升策略。",
          "suggestion": "设计高质量运算变式训练。"
        },
        {
          "name": "3.6数据分析（整体）",
          "score": 91,
          "advantage": "研究学习可能涉及数据处理。",
          "disadvantage": "未系统体现数据分析素养。",
          "suggestion": "在数学与跨学科项目中强化数据收集与分析。"
        }
      ]
    }
  ]
}`
	return fakeAnswer, nil, nil
}
