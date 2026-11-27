import { get, post, put, patch, del } from '@/utils/request'
export type IssueKind='missing'|'bug'|'suggestion'
export type IssueStatus='open'|'inprogress'|'resolved'|'closed'
export interface OctoIssue {
  id:string; kind:IssueKind; title:string; description:string; expected:string;steps:string;is_direct:boolean; knowledge_base_id:string; scope_id:string; scope_name:string;
  group_id:string; subarea_id:string; reporter_uid:string; reporter_name:string; message_id:string; original_message:string;
  attachments:Array<{name:string;url:string;type:string}>; owner_uid:string; owner_name:string; status:IssueStatus; created_at:string; updated_at:string;
  archive_state?: 'archived'|'trash'|''
}
export interface OctoContact {id:string;knowledge_base_id:string;topic:string;name:string;uid:string;details:string;is_default:boolean}
export interface IssueFilter {scope_id?:string;knowledge_base_id?:string;kind?:string;status?:string;keyword?:string;from?:string;to?:string;page?:number;page_size?:number;owner_uid?:string;visibility?:string}
type Envelope<T>={success:boolean;data:T}
const root='/api/v1/octo/business'
export const issueQuery=(filter:IssueFilter)=>new URLSearchParams(Object.entries(filter).filter(([,v])=>v!==undefined&&v!=='').map(([k,v])=>[k,String(v)])).toString()
export const listIssues=(filter:IssueFilter)=>get(`${root}/issues?${issueQuery(filter)}`) as unknown as Promise<Envelope<{items:OctoIssue[];total:number}>>
export interface IssueEvent {actor_uid:string;actor_name:string;status:IssueStatus;note:string;created_at:string}
export interface IssueEvidence {question?:string;question_message_id?:string;answer?:string;answer_message_id?:string;materials?:{label:string;text?:string;status:string;path?:string;version?:string}[]}
export interface IssueRetest {id:string;answer:string;model_id:string;sources:string[]}
export interface IssueRetestRecord extends IssueRetest {
  scenario?:string; question?:string; plan_revision?:number; document_id?:string; source_version?:string; document_cited?:boolean
  state:string;error_code:string;review_state:string;review_note:string;created_at:string;reviewed_at?:string
}
const issueRetestPath=(id:string)=>`${root}/issues/${encodeURIComponent(id)}/retests`
export const issueRetests=(id:string)=>get<Envelope<IssueRetestRecord[]>>(issueRetestPath(id))
export const reviewIssueRetest=(id:string,run:string,state:string,note:string)=>
  post<Envelope<IssueRetestRecord>>(`${issueRetestPath(id)}/${encodeURIComponent(run)}/review`,{state,note})
export const issueDetail=(id:string)=>get(`${root}/issues/${encodeURIComponent(id)}`) as unknown as Promise<Envelope<{issue:OctoIssue;events:IssueEvent[];evidence?:IssueEvidence|null}>>
export const updateIssue=(id:string, data:{status:IssueStatus;owner_uid?:string;owner_name?:string;owner_contact_id?:string;note:string})=>patch(`${root}/issues/${encodeURIComponent(id)}`,data)
export const retestIssue=(id:string,scenario='original',revision=0)=>post<Envelope<IssueRetest>>(`${root}/issues/${encodeURIComponent(id)}/retest`, {scenario,revision}, { timeout:95000 })
export interface RepairSource {document_id:string;version:string;path:string}
export interface RepairPlan {cause:string;acceptance:string;paraphrase:string;counterexample:string;wait_for_update:boolean}
export interface RepairView {revision:number;coordinator:string;state:string;verification_mode?:'lightweight'|'strict';plan:RepairPlan & {source?:RepairSource};published?:RepairSource}
const repairPath=(id:string)=>`${root}/issues/${encodeURIComponent(id)}/remediation`
export const issueRepair=(id:string)=>get<Envelope<RepairView>>(repairPath(id))
export const saveIssueRepair=(id:string,plan:RepairPlan & {revision:number;document_id:string})=>put<Envelope<RepairView>>(repairPath(id),plan)
export const setIssueRemediationMode=(id:string,mode:'lightweight'|'strict',reason='')=>patch<Envelope<RepairView>>(`${repairPath(id)}/mode`,{mode,reason})
export interface IssueReply {
  id:string;actor_uid:string;channel_id:string;scope_name:string;target:string;user_name:string;user_uid:string
  message_id:string;body:string;status:string;receipt:string;created_at:string
}
const replyPath=(id:string)=>`${root}/issues/${encodeURIComponent(id)}/replies`
export const issueReplies=(id:string)=>get<Envelope<IssueReply[]>>(replyPath(id))
export const previewIssueReply=(id:string, target:string, body:string)=>post<Envelope<IssueReply>>(replyPath(id),{target,body})
export const sendIssueReply=(id:string,reply:string)=>post<Envelope<IssueReply>>(`${replyPath(id)}/${encodeURIComponent(reply)}/send`,{}, {timeout:40000})
export interface OperationResult { id:string; state:string }
export const batchIssues=(body:{targets:{id:string;updated_at:string}[];action:string;status?:string;contact_id?:string;note?:string})=>
  post<Envelope<OperationResult[]>>(`${root}/issues/batch`,body)
export interface QualityRow {
  id: string; question: string; answer: string; outcome: string; created_at: string
  unanswered_repeats: number; group_key: string; unavailable_sources: number; sources?: {library: string; wiki_slug?: string; chunk_id?: string}[]
  review?: { state:string; suggestion:string; reason:string; disposition:string; topic:string; issue_id:string; library_id:string }
  source_type?: 'group'|'subarea'|'dm'|''
  scope_id?: string; scope_name?: string; channel_id?: string; bot_name?: string; asker_name?: string
  interrupted?: boolean; agent_trace?: string[]
}
export interface QualityStats {
  questions:number; users:number; reviewed:number; needs_review:number; unavailable:number; failed:number
  days:{label:string;count:number}[]; topics:{label:string;count:number}[]
  groups:QualityGroup[]
  similar:{a:QualityGroup;b:QualityGroup}[]
}
export interface QualityGroup {id:string;question:string;count:number;users:number;gaps:number;example_id:string}
export interface QualityPage {
  rows: QualityRow[]; libraries: {id: string; name: string}[]
  sample_size: number; since: string; until:string; truncated: boolean; total:number; stats:QualityStats
}
export interface QualityDraft {
  id?: string; expires_at?: string
  finding: { category: string; title: string; reason: string; suggestion: string; evidence: {ref: string; quote: string}[] }
  excerpts: {id: string; label: string; text?: string; status?: string}[]
}
export interface QualityFilter { library_id?:string; keyword?:string; filter?:string; topic?:string; group?:string; from?:string; to?:string; page?:number; page_size?:number; source_type?:string; channel_id?:string; include_dm?:boolean }
export const listQuality=(scope: string, filter:QualityFilter={})=>{
  const query = new URLSearchParams(Object.entries({scope_id:scope,...filter}).filter(([, val])=>val!==undefined&&val!==''&&val!==false).map(([key,val])=>[key,String(val)]))
  return get<Envelope<QualityPage>>(`${root}/quality?${query}`)
}
export const convertQualityToIssue=(messageId: string, data: {scope_id?:string; channel_id?:string; is_direct?:boolean; title:string; knowledge_base_id:string; kind:IssueKind; note?:string; contact_id?:string})=>
  post<Envelope<OctoIssue>>(`${root}/quality/${encodeURIComponent(messageId)}/convert-issue`, data)
export const retryFailedQuality=(data: {scope_id?:string; message_id?:string}={})=>
  post<Envelope<{retried:number}>>(`${root}/quality/retry-failed`, data)
export const analyzeQuality=(data: {scope_id: string; library_id: string; message_ids: string[];use_review?:boolean})=>
  post<Envelope<QualityDraft>>(`${root}/quality/analyze`, data, { timeout:75000 })
export const confirmQuality=(id: string,data: {title: string; note: string; contact_id: string})=>
  post<Envelope<OctoIssue>>(`${root}/quality/${encodeURIComponent(id)}/confirm`, data)
export const batchQuality=(body:{scope_id:string; library_id?:string; message_ids:string[]; action:string; value:string})=>
  post<Envelope<OperationResult[]>>(`${root}/quality/batch`,body)
export interface QualitySettings {enabled:boolean;model_id:string;per_run:number;daily_limit:number;sample_percent:number}
export const qualitySettings=()=>get<Envelope<QualitySettings>>(`${root}/quality/settings`)
export const saveQualitySettings=(body:QualitySettings)=>put<Envelope<QualitySettings>>(`${root}/quality/settings`,body)
export const listContacts=(kb:string)=>get(`${root}/contacts?knowledge_base_id=${encodeURIComponent(kb)}`) as unknown as Promise<Envelope<OctoContact[]>>
export const saveContact=(data:OctoContact)=>put(`${root}/contacts`,data)
export const deleteContact=(id:string)=>del(`${root}/contacts/${encodeURIComponent(id)}`)
export const issueReport=(filter:IssueFilter)=>get(`${root}/report?${issueQuery(filter)}`) as unknown as Promise<Envelope<{counts:Record<string,number>;items:OctoIssue[];total:number;truncated:boolean;from?:string;to?:string;generated_at:string;scope:string;notice:string}>>
export interface DeliveryTarget {
  key: string
  kind: 'group' | 'dm'
  group_id?: string
  subarea_id?: string
  uid?: string
  label: string
  channel_kind: string
  bound_kb_count?: number
  inherited?: boolean
}
export interface ReportSchedule {
  id:string;name:string;channel_id:string;scope_id:string;recipient_type:'source'|'private';recipient_uid:string
  weekday:number;hour:number;minute:number;timezone:string;format:'text'|'markdown'|'html'|'csv';enabled:boolean
  next_run_at?:string;last_run_at?:string;last_status?:string;last_error?:string
  template:string;frequency:string;period:string;mode:string;lookback:number;interval_days:number;month_day:number
  threshold:number;minimum_users:number;only_changes:boolean;instruction:string
  topic:string;quiet_start:number;quiet_end:number;cooldown_hours:number
  sender_channel_id?:string;delivery_targets?:DeliveryTarget[];scope_filter_json?:string
}
export const listReportSchedules=()=>get(`${root}/report-schedules`) as unknown as Promise<Envelope<ReportSchedule[]>>
export const saveReportSchedule=(data:ReportSchedule)=>data.id?put(`${root}/report-schedules/${encodeURIComponent(data.id)}`,data):post(`${root}/report-schedules`,data)
export const deleteReportSchedule=(id:string)=>del(`${root}/report-schedules/${encodeURIComponent(id)}`)
export interface ReportDates { from?:string;to?:string }
export interface ReportPreview extends ReportDates { next:string;authorized:boolean;code:string }
export interface ReportRun extends ReportDates {
  id:string; schedule_id:string;status:string;due_at:string;target:string;summary:string;error_code:string
}
export interface ReportRunDelivery {
  id:string;run_id:string;schedule_id:string;sender_channel:string;target_key:string;target_kind:'group'|'dm'
  target_label:string;status:'pending'|'sending'|'sent'|'failed'|'uncertain';receipt:string;error_code:string;error_message:string;attempt_count:number;sent_at?:string
}
export interface ReportRunViewV3 extends ReportRun {
  sender_channel_id:string;schedule_name:string;template:string;deliveries:ReportRunDelivery[]
}
export interface BotDeliveryGroup {
  key:string;group_id:string;subarea_id:string;kind:'主群'|'子区';label:string;bound_kb_count:number;inherited:boolean
}
export interface BotDMCandidate { uid:string;name:string;source:'recent_dm'|'contact'|'creator' }
export interface BotDeliveryTargetsResponse {
  channel_id:string;account_id:string;bot_name:string;groups:BotDeliveryGroup[];dm_candidates:BotDMCandidate[]
}
export interface BotFriendVerifyResult {
  ok:boolean;uid:string;name:string;is_creator:boolean;reason_code?:string;reason_message?:string;suggested_bots?:string[]
}
export interface WorkbenchKBHealth {
  id:string;name:string;docs:number;ready:number;disabled:number;pptx:number;xlsx:number;wiki_pages:number
  sync_status:'ok'|'partial'|'failed'|'none';partial_runs_14d:number;last_sync_at:string;schedule:string;sync_note:string
}
export interface WorkbenchActionItem {
  kind:'qa'|'issue';priority:number;id:string;title:string;badge:string;badge_tone:'err'|'warn'|'brand'|'ok'
  source_type:string;place:string;scope_id:string;channel_id:string;knowledge_base_id:string;knowledge_base_name:string
  meta_top:string;meta_sub:string;sub_text:string;cta:string;primary:boolean
}
export interface WorkbenchSummary {
  since:string;until:string
  alerts:{ interrupted_count:number;failed_ai_reviews:number;partial_sync_kbs:WorkbenchKBHealth[] }
  kpis:{ ai_process_count:number;ai_converted_count:number;open_issues:number;replied_issues:number;interrupted_count:number;failed_ai_reviews:number;interrupted_rate:number;kb_count:number;wiki_pages:number;pptx_count:number;xlsx_count:number }
  action_stream:WorkbenchActionItem[]
  kb_health:WorkbenchKBHealth[]
  hot_questions:Array<{id:string;question:string;knowledge_base_name:string;source_type:string;count:number;users:number}>
  pending_qa:number;open_issues:number
}
export const workbenchSummary=(params:Record<string,string|undefined>)=>{
  const q=new URLSearchParams(Object.entries(params).filter(([,v])=>Boolean(v)).map(([k,v])=>[k,String(v)]))
  return get<Envelope<WorkbenchSummary>>(`${root}/workbench/summary?${q}`)
}
export const botDeliveryTargets=(channelId:string)=>get<Envelope<BotDeliveryTargetsResponse>>(`${root}/bots/${encodeURIComponent(channelId)}/delivery-targets`)
export const verifyBotFriend=(channelId:string,uid:string)=>post<Envelope<BotFriendVerifyResult>>(`${root}/bots/${encodeURIComponent(channelId)}/verify-friend`,{uid})
export const createAdhocReportRun=(body:{title?:string;sender_channel_id:string;delivery_targets:DeliveryTarget[];format?:string;custom_note?:string;scope_id?:string;knowledge_base_id?:string;from?:string;to?:string})=>
  post<Envelope<ReportRunViewV3>>(`${root}/reports/adhoc-runs`,body,{timeout:90000})
export const listAllReportRuns=(status='')=>get<Envelope<ReportRunViewV3[]>>(`${root}/report-runs${status?`?status=${encodeURIComponent(status)}`:''}`)
export const sendReportRunV3=(run:string)=>post<Envelope<ReportRunViewV3>>(`${root}/report-runs/${encodeURIComponent(run)}/send`,{},{timeout:125000})
export const retryReportDelivery=(run:string,deliveryId:string)=>post<Envelope<ReportRunViewV3>>(`${root}/report-runs/${encodeURIComponent(run)}/deliveries/${encodeURIComponent(deliveryId)}/retry`,{},{timeout:90000})
export const previewReportSchedule=(data:ReportSchedule & ReportDates)=>post<Envelope<ReportPreview>>(`${root}/report-schedules/preview`,data)
const runsPath=(id:string)=>`${root}/report-schedules/${encodeURIComponent(id)}/runs`
export const listReportRuns=(id:string)=>get<Envelope<ReportRun[]>>(runsPath(id))
export const generateReportRun=(id:string,dates:ReportDates)=>post<Envelope<ReportRun>>(runsPath(id),dates,{timeout:125000})
export const detailReportRun=(id:string,run:string)=>get<Envelope<ReportRun>>(`${runsPath(id)}/${encodeURIComponent(run)}`)
export const deliverReportRun=(id:string,run:string)=>post<Envelope<ReportRun>>(`${runsPath(id)}/${encodeURIComponent(run)}/send`,{},{timeout:125000})
