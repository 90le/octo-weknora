export const issueKindLabels: Record<string,string> = {missing:'知识缺口',bug:'Bug 反馈',suggestion:'建议'}
export const issueStatusLabels: Record<string,string> = {open:'待处理',inprogress:'处理中',resolved:'已解决',closed:'已关闭'}
export function displayPerson(name?:string,uid?:string):string {return name?.trim() || uid?.trim() || '未指定'}
// Spreadsheet applications interpret formula-looking cells even in quoted CSV.
export function csvCell(value:unknown):string {
  let text=String(value??'');if(/^[\s]*[=+@-]/.test(text))text=`'${text}`
  return `"${text.replace(/"/g,'""')}"`
}
export function safeAttachmentURL(raw:string):string|null {
  try {const url=new URL(raw);return url.protocol==='https:'||url.protocol==='http:'?url.href:null}catch{return null}
}
