// 折叠预览纯函数（从 thinking-block.tsx 抽出：组件文件保持 components-only，
// react-refresh/HMR 才能保留组件状态）。单测直引此处，禁止在测试里抄一份实现。
/** 折叠预览纯函数：流式取实时尾巴，结束取开头一行。 */
export function thinkingPreview(thinking: string, streaming?: boolean): string {
  return streaming
    ? thinking.trimEnd().slice(-80)
    : thinking.split("\n")[0].slice(0, 80)
}
