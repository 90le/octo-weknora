import { get, post, put, patch, del } from '@/utils/request'
export type IssueKind='missing'|'bug'|'suggestion'
export type IssueStatus='open'|'inprogress'|'resolved'|'closed'
export interface OctoIssue {
  id:string; kind:IssueKind; title:string; description:string; expected:string;steps:string;is_direct:boolean; knowledge_base_id:string; scope_id:string; scope_name:string;
  group_id:string; subarea_id:string; reporter_uid:string; reporter_name:string; message_id:string; original_message:string;
  attachments:Array<{name:string;url:string;type:string}>; owner_uid:string; owner_name:string; status:IssueStatus; created_at:string; updated_at:string;
}
export interface OctoContact {id:string;knowledge_base_id:string;topic:string;name:string;uid:string;details:string;is_default:boolean}
export interface IssueFilter {scope_id?:string;knowledge_base_id?:string;kind?:string;status?:string;keyword?:string;from?:string;to?:string;page?:number;page_size?:number}
type Envelope<T>={success:boolean;data:T}
const root='/api/v1/octo/business'
export const issueQuery=(filter:IssueFilter)=>new URLSearchParams(Object.entries(filter).filter(([,v])=>v!==undefined&&v!=='').map(([k,v])=>[k,String(v)])).toString()
export const listIssues=(filter:IssueFilter)=>get(`${root}/issues?${issueQuery(filter)}`) as unknown as Promise<Envelope<{items:OctoIssue[];total:number}>>
export interface IssueEvent {actor_uid:string;actor_name:string;status:IssueStatus;note:string;created_at:string}
export const issueDetail=(id:string)=>get(`${root}/issues/${encodeURIComponent(id)}`) as unknown as Promise<Envelope<{issue:OctoIssue;events:IssueEvent[]}>>
export const updateIssue=(id:string, data:{status:IssueStatus;owner_uid:string;owner_name:string;note:string})=>patch(`${root}/issues/${encodeURIComponent(id)}`,data)
export const listContacts=(kb:string)=>get(`${root}/contacts?knowledge_base_id=${encodeURIComponent(kb)}`) as unknown as Promise<Envelope<OctoContact[]>>
export const saveContact=(data:OctoContact)=>put(`${root}/contacts`,data)
export const deleteContact=(id:string)=>del(`${root}/contacts/${encodeURIComponent(id)}`)
export const issueReport=(filter:IssueFilter)=>get(`${root}/report?${issueQuery(filter)}`) as unknown as Promise<Envelope<{counts:Record<string,number>;items:OctoIssue[];total:number;truncated:boolean;from?:string;to?:string;generated_at:string;scope:string;notice:string}>>
export interface ReportSchedule {id:string;name:string;channel_id:string;scope_id:string;recipient_type:'source'|'private';recipient_uid:string;weekday:number;hour:number;minute:number;timezone:string;format:'text'|'markdown'|'html'|'csv';enabled:boolean;next_run_at?:string;last_run_at?:string;last_status?:string;last_error?:string}
export const listReportSchedules=()=>get(`${root}/report-schedules`) as unknown as Promise<Envelope<ReportSchedule[]>>
export const saveReportSchedule=(data:ReportSchedule)=>data.id?put(`${root}/report-schedules/${encodeURIComponent(data.id)}`,data):post(`${root}/report-schedules`,data)
export const deleteReportSchedule=(id:string)=>del(`${root}/report-schedules/${encodeURIComponent(id)}`)
