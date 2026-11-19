// 统一的高亮语言子集：聊天 markdown（rehype-highlight/lowlight）与
// mqtt-form 代码块（highlight.js/lib/core）共用一份 10 语言注册表，
// 替代 lowlight 默认 common 全集（~36 语言，lib chunk 显著缩水）。
// 选语言与聊天消息里实际出现的代码围栏对齐（go/ts/py/bash/json/yaml
// 等）；新增语言在此一处加。
import bash from "highlight.js/lib/languages/bash"
import go from "highlight.js/lib/languages/go"
import java from "highlight.js/lib/languages/java"
import javascript from "highlight.js/lib/languages/javascript"
import json from "highlight.js/lib/languages/json"
import python from "highlight.js/lib/languages/python"
import sql from "highlight.js/lib/languages/sql"
import typescript from "highlight.js/lib/languages/typescript"
import xml from "highlight.js/lib/languages/xml"
import yaml from "highlight.js/lib/languages/yaml"

export const codeLanguages = {
  bash,
  go,
  java,
  javascript,
  json,
  python,
  sql,
  typescript,
  xml,
  yaml,
} as const
