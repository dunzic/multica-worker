// Synthetic HR acceptance fixture; never a built-in action in the generic composer.
export const HR_BATCH_REVIEW_PROMPT = `请按我提供的岗位 JD 筛选本批简历。
如果尚未提供 JD，请先询问，不要自行编造筛选标准。
只依据岗位相关的技能、经验等可核验信息评估；不要推断敏感个人属性，不以性别、年龄或婚育情况作为筛选标准。
逐份读取所有附件，为每份简历单独返回：候选人、文件名、与 JD 的匹配证据、缺失信息、建议以及原文件预览链接。
给出批次汇总：文件总数、已处理数、失败或不支持的文件及原因。不要跳过失败项，也不要把无法解析的文件当作已筛选。
请在当前工作区建立一个批次总任务，每份简历建立关联的筛选子任务；保留原文件附件关联，在对话中返回总任务、各子任务和原文件预览链接。
只在当前工作区创建任务并遵循现有访问权限；HR 做最终招聘决定。`;

export function hrBatchReviewForAgent(id: string, name: string): string {
  // A display name must not inject additional Markdown mention targets.
  const label = name.replace(/[[\]()\\\r\n]/g, " ").trim().slice(0, 120) || "HR";
  return `[@${label}](mention://agent/${id})\n\n${HR_BATCH_REVIEW_PROMPT}`;
}
