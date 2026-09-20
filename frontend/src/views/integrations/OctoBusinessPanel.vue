<template>
  <section class="business-panel">
    <header><h2>问题、反馈与联系人</h2><p>查看 Bot 已登记的真实问题，维护处理记录和产品联系人。仅提供联系方式，不自动通知或催办。</p></header>
    <t-tabs v-model="tab">
      <t-tab-panel value="issues" label="问题与反馈">
        <div class="filters">
          <t-input v-model="filter.keyword" clearable placeholder="搜索问题标题或描述" @enter="search" />
          <t-select v-model="filter.scope_id" clearable filterable placeholder="所有区域（工作区管理员）" :options="scopeOptions" @change="search" />
          <t-select v-model="filter.knowledge_base_id" clearable filterable placeholder="所有知识库" :options="kbOptions" @change="search" />
          <t-select v-model="filter.kind" clearable placeholder="所有类型" :options="kindOptions" @change="search" />
          <t-select v-model="filter.status" clearable placeholder="所有状态" :options="statusOptions" @change="search" />
        </div>
        <div class="toolbar"><label>登记日期（本地时间）<input v-model="fromDay" type="date" aria-label="开始日期" /></label><span>至</span><input v-model="toDay" type="date" aria-label="结束日期（含当天）" /><t-button :loading="loading" @click="search">查询</t-button><t-button variant="outline" @click="resetFilters">重置</t-button><t-button variant="outline" :loading="reportLoading" @click="previewReport">预览汇总</t-button></div>
        <t-alert v-if="error" theme="error">{{ error }} <t-button variant="text" @click="loadIssues">重试</t-button></t-alert>
        <t-table :data="issues" :columns="columns" row-key="id" :loading="loading" :empty="error ? '读取失败，不能视为没有问题' : '暂无符合条件的已登记问题'">
          <template #title="{row}"><t-button variant="text" class="issue-title" @click="openIssue(row.id)">{{ row.title }}</t-button><small>{{ issueKindLabels[row.kind] || row.kind }}</small></template>
          <template #scope="{row}"><span>{{ row.is_direct ? '私聊' : (row.scope_name || '未命名区域') }}</span><small>{{ kbName(row.knowledge_base_id) }}</small></template>
          <template #reporter="{row}"><span>{{ displayPerson(row.reporter_name,row.reporter_uid) }}</span><small>{{ row.reporter_uid }}</small></template>
          <template #owner="{row}">{{ displayPerson(row.owner_name,row.owner_uid) }}</template>
          <template #status="{row}"><t-tag size="small" :theme="row.status==='resolved'?'success':row.status==='closed'?'default':'warning'">{{ issueStatusLabels[row.status] || row.status }}</t-tag></template>
          <template #created_at="{row}">{{ time(row.created_at) }}</template>
        </t-table>
        <t-pagination v-if="total" v-model="page" :total="total" :page-size="20" :show-page-size="false" @change="loadIssues" />
        <p class="hint">仅统计已登记问题，不代表全部提问量；“已关闭”不表示知识已更新。</p>
      </t-tab-panel>
      <t-tab-panel value="contacts" label="知识库联系人">
        <div class="toolbar"><t-select v-model="contactKB" :options="kbOptions" filterable clearable placeholder="选择要维护联系人的知识库" @change="loadContacts" /><t-button :disabled="!contactKB" @click="newContact">添加联系人</t-button></div>
        <t-alert v-if="contactError" theme="error">{{ contactError }} <t-button variant="text" @click="loadContacts">重试</t-button></t-alert>
        <t-loading :loading="contactsLoading"><t-empty v-if="!contacts.length" :description="contactKB ? '尚未配置联系人。可按产品主题配置多位联系人，并设置一个默认联系人。' : '先选择知识库'" /><div class="contact-grid"><article v-for="c in contacts" :key="c.id"><div class="row"><strong>{{ c.name }}</strong><t-tag v-if="c.is_default" size="small" theme="primary">默认联系人</t-tag></div><p>{{ c.topic || '通用问题' }}</p><small>原生 UID：{{ c.uid }}</small><p class="prewrap">{{ c.details }}</p><div class="row"><t-button variant="text" @click="editContact(c)">编辑</t-button><t-popconfirm content="删除后 Bot 不再推荐此联系人。不会删除用户或发出通知。" @confirm="removeContact(c.id)"><t-button theme="danger" variant="text" :disabled="contactSaving">删除</t-button></t-popconfirm></div></article></div></t-loading>
      </t-tab-panel>
      <t-tab-panel value="schedule" label="自动周报"><OctoReportSchedules :scopes="scopes" /></t-tab-panel>
    </t-tabs>
    <t-drawer v-model:visible="detailVisible" header="问题详情与处理" size="680px" :footer="false">
      <t-loading :loading="detailLoading"><t-alert v-if="detailError" theme="error">{{ detailError }}</t-alert><template v-if="selected">
        <h3>{{ selected.title }}</h3><div class="meta"><span>{{ issueKindLabels[selected.kind] }}</span><span>{{ selected.is_direct?'私聊':selected.scope_name }}</span><span>{{ time(selected.created_at) }}</span></div>
        <dl><dt>提问人</dt><dd>{{ displayPerson(selected.reporter_name,selected.reporter_uid) }} · {{ selected.reporter_uid }}</dd><dt>知识库</dt><dd><router-link :to="{name:'knowledgeBaseDetail',params:{kbId:selected.knowledge_base_id}}">{{ kbName(selected.knowledge_base_id) }}</router-link></dd><dt>消息 ID</dt><dd>{{ selected.message_id || '无' }}</dd><dt>记录编号</dt><dd>{{ selected.id }}</dd></dl>
        <h4>问题描述</h4><p class="prewrap">{{ selected.description }}</p>
        <template v-if="selected.steps"><h4>复现步骤</h4><p class="prewrap">{{ selected.steps }}</p></template><template v-if="selected.expected"><h4>预期结果</h4><p class="prewrap">{{ selected.expected }}</p></template>
        <h4>原始消息</h4><blockquote class="prewrap">{{ selected.original_message }}</blockquote>
        <ul v-if="selected.attachments?.length"><li v-for="(a,i) in selected.attachments" :key="i"><a v-if="safeAttachmentURL(a.url)" :href="safeAttachmentURL(a.url)!" target="_blank" rel="noopener noreferrer">{{ a.name || '附件' }}</a><span v-else>{{ a.name || '附件（链接不可用）' }}</span></li></ul>
        <h4>更新处理</h4><label>状态<t-select v-model="issueEdit.status" :options="statusOptions" /></label><div class="two-col"><label>负责人姓名<t-input v-model="issueEdit.owner_name" :maxlength="256" /></label><label>负责人原生 UID<t-input v-model="issueEdit.owner_uid" :maxlength="128" /></label></div><label>处理说明<t-textarea v-model="issueEdit.note" :maxlength="4000" placeholder="记录处理结果、资料链接或关闭原因" /></label><p class="hint">这里只更新问题记录，不修改知识库，也不会通知负责人。</p><t-button :loading="issueSaving" @click="saveIssue">保存处理记录</t-button>
        <h4>处理记录</h4><ol class="history"><li v-for="(event,i) in events" :key="i"><strong>{{ issueStatusLabels[event.status] || event.status }}</strong> · {{ displayPerson(event.actor_name,event.actor_uid) }}<small>{{ time(event.created_at) }}</small><p class="prewrap">{{ event.note || '无补充说明' }}</p></li></ol>
      </template></t-loading>
    </t-drawer>
    <t-dialog v-model:visible="contactVisible" :header="contactEdit.id?'编辑联系人':'添加联系人'" :confirm-loading="contactSaving" @confirm="submitContact">
      <label>姓名<t-input v-model="contactEdit.name" :maxlength="256" /></label><label>原生 UID<t-input v-model="contactEdit.uid" :maxlength="128" placeholder="用于 Octo 原生用户名片" /></label><label>负责主题<t-input v-model="contactEdit.topic" :maxlength="256" placeholder="例如合同、计费、产品使用" /></label><label>联系方式与备注<t-textarea v-model="contactEdit.details" :maxlength="2000" placeholder="电话、邮箱、办公时间或联系说明" /></label><label class="row"><t-switch v-model="contactEdit.is_default" />作为该知识库默认联系人</label><p class="hint">设置默认会替换该知识库原默认联系人。其他联系人保留。</p>
    </t-dialog>
    <t-drawer v-model:visible="reportVisible" header="登记问题汇总" size="620px" :footer="false"><template v-if="report"><p>{{ reportRange }}</p><p>生成时间：{{ time(report.generated_at) }}</p><div class="stat-grid"><div><strong>{{ report.total }}</strong><small>符合条件的登记问题</small></div><div v-for="(label,status) in issueStatusLabels" :key="status"><strong>{{ report.counts[status] || 0 }}</strong><small>{{ label }}</small></div></div><t-alert v-if="report.truncated" theme="warning">明细最多显示最近 500 项，统计包含全部结果。请缩短时间或选择单个区域后再导出。</t-alert><p class="hint">按创建时间筛选，截止日期含当天。只统计已登记的问题；关闭不等于知识更新。此预览不会自动发送。</p><t-button @click="downloadReport">下载当前明细 CSV</t-button><ul><li v-for="item in report.items" :key="item.id"><strong>{{ item.title }}</strong><p>{{ issueStatusLabels[item.status] }} · {{ displayPerson(item.owner_name,item.owner_uid) }} · {{ item.scope_name || '私聊' }}</p></li></ul></template></t-drawer>
  </section>
</template>
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { listKnowledgeBases } from '@/api/knowledge-base'
import { listScopes, type OctoScope } from '@/api/octo'
import { listIssues,issueDetail,updateIssue,listContacts,saveContact,deleteContact,issueReport,type OctoIssue,type OctoContact,type IssueEvent,type IssueStatus,type IssueFilter } from '@/api/octo-business'
import { issueKindLabels,issueStatusLabels,displayPerson,csvCell,safeAttachmentURL } from './octoBusinessDisplay'
import { useAuthStore } from '@/stores/auth'
import { useRoute } from 'vue-router'
import OctoReportSchedules from './OctoReportSchedules.vue'
const auth=useAuthStore(),tab=ref('issues'),filter=ref<IssueFilter>({}),fromDay=ref(''),toDay=ref(''),page=ref(1),total=ref(0)
const route=useRoute()
const scopes=ref<OctoScope[]>([]),kbs=ref<Array<{id:string;name:string}>>([]),issues=ref<OctoIssue[]>([]),loading=ref(false),error=ref('')
const kindOptions=Object.entries(issueKindLabels).map(([value,label])=>({value,label})),statusOptions=Object.entries(issueStatusLabels).map(([value,label])=>({value,label}))
const scopeOptions=computed(()=>scopes.value.map(s=>({value:s.id,label:`${s.subarea_id?'子区':'主群'} · ${s.display_name} · ${s.account_id}`}))),kbOptions=computed(()=>kbs.value.map(k=>({value:k.id,label:k.name})))
const kbName=(id:string)=>kbs.value.find(k=>k.id===id)?.name||id
const time=(value:string)=>value?new Date(value).toLocaleString():'—'
const columns=[{colKey:'title',title:'问题',minWidth:220},{colKey:'scope',title:'来源／知识库',minWidth:180},{colKey:'reporter',title:'提问人',minWidth:150},{colKey:'owner',title:'负责人',minWidth:100},{colKey:'status',title:'状态',width:100},{colKey:'created_at',title:'登记时间',minWidth:170}]
let listVersion=0,metadataVersion=0,detailVersion=0,contactVersion=0,reportVersion=0
function query():IssueFilter{const f={...filter.value};if(fromDay.value)f.from=new Date(`${fromDay.value}T00:00:00`).toISOString();if(toDay.value){const d=new Date(`${toDay.value}T00:00:00`);d.setDate(d.getDate()+1);f.to=d.toISOString()}if(f.from&&f.to&&f.from>=f.to)throw new Error('开始日期不能晚于结束日期');return f}
async function metadata(){const v=++metadataVersion;try{const [k,s]=await Promise.all([listKnowledgeBases(),listScopes()]);if(v===metadataVersion){kbs.value=k.data||[];scopes.value=s.data;let offset=100;while(s.data.length===100&&v===metadataVersion){const batch=await listScopes(offset);if(v!==metadataVersion)return;scopes.value.push(...batch.data);if(batch.data.length<100)break;offset+=100}}}catch{if(v===metadataVersion)error.value='配置列表加载失败，请刷新页面后重试。'}}
async function loadIssues(){const v=++listVersion;loading.value=true;error.value='';try{const result=await listIssues({...query(),page:page.value,page_size:20});if(v===listVersion){issues.value=result.data.items;total.value=result.data.total}}catch(e){if(v===listVersion){issues.value=[];total.value=0;error.value=e instanceof Error&&e.message.includes('日期')?e.message:'问题列表读取失败，请检查权限和服务状态。'}}finally{if(v===listVersion)loading.value=false}}
function search(){page.value=1;void loadIssues()}
function resetFilters(){filter.value={};fromDay.value='';toDay.value='';search()}
const selected=ref<OctoIssue|null>(null),events=ref<IssueEvent[]>([]),detailVisible=ref(false),detailLoading=ref(false),detailError=ref(''),issueSaving=ref(false)
const issueEdit=ref<{status:IssueStatus;owner_uid:string;owner_name:string;note:string}>({status:'open',owner_uid:'',owner_name:'',note:''})
async function openIssue(id:string){const v=++detailVersion;detailVisible.value=true;detailLoading.value=true;detailError.value='';selected.value=null;events.value=[];try{const result=await issueDetail(id);if(v!==detailVersion)return;selected.value=result.data.issue;events.value=result.data.events;issueEdit.value={status:selected.value.status,owner_uid:selected.value.owner_uid,owner_name:selected.value.owner_name,note:''}}catch{if(v===detailVersion)detailError.value='读取详情失败，记录可能已删除或权限已变更。'}finally{if(v===detailVersion)detailLoading.value=false}}
async function saveIssue(){if(!selected.value||issueSaving.value)return;const id=selected.value.id;if(Boolean(issueEdit.value.owner_uid.trim())!==Boolean(issueEdit.value.owner_name.trim())){await MessagePlugin.warning('负责人姓名和 UID 请一同填写，或一同清空');return}issueSaving.value=true;try{await updateIssue(id,issueEdit.value);await Promise.all([openIssue(id),loadIssues()]);await MessagePlugin.success('处理记录已保存')}catch{await MessagePlugin.error('保存失败，请检查权限后重试')}finally{issueSaving.value=false}}
const contacts=ref<OctoContact[]>([]),contactKB=ref(''),contactsLoading=ref(false),contactSaving=ref(false),contactVisible=ref(false),contactError=ref('')
const blankContact=():OctoContact=>({id:'',knowledge_base_id:contactKB.value,topic:'',name:'',uid:'',details:'',is_default:false}),contactEdit=ref<OctoContact>(blankContact())
async function loadContacts(){const v=++contactVersion;contacts.value=[];contactError.value='';contactsLoading.value=false;if(!contactKB.value)return;contactsLoading.value=true;try{const result=await listContacts(contactKB.value);if(v===contactVersion)contacts.value=result.data}catch{if(v===contactVersion)contactError.value='联系人加载失败，请检查知识库权限。'}finally{if(v===contactVersion)contactsLoading.value=false}}
function newContact(){contactEdit.value=blankContact();contactVisible.value=true}function editContact(c:OctoContact){contactEdit.value={...c};contactVisible.value=true}
async function submitContact(){if(contactSaving.value)return;if(!contactEdit.value.name.trim()||!contactEdit.value.uid.trim()){await MessagePlugin.warning('请填写联系人姓名和原生 UID');return}contactSaving.value=true;try{await saveContact(contactEdit.value);contactVisible.value=false;await loadContacts();await MessagePlugin.success('联系人已保存')}catch{await MessagePlugin.error('联系人保存失败，请检查权限和填写内容')}finally{contactSaving.value=false}}
async function removeContact(id:string){if(contactSaving.value)return;contactSaving.value=true;try{await deleteContact(id);await loadContacts()}catch{await MessagePlugin.error('删除失败，联系人保持不变')}finally{contactSaving.value=false}}
type Report=Awaited<ReturnType<typeof issueReport>>['data']
const report=ref<Report|null>(null),reportVisible=ref(false),reportLoading=ref(false),reportRange=ref('')
async function previewReport(){const v=++reportVersion;reportLoading.value=true;try{const f=query();const result=await issueReport(f);if(v!==reportVersion)return;report.value=result.data;reportRange.value=`登记日期：${fromDay.value||'不限起始'} 至 ${toDay.value||'当前'}；区域：${scopeOptions.value.find(s=>s.value===f.scope_id)?.label||'工作区授权区域'}`;reportVisible.value=true}catch{await MessagePlugin.error('汇总生成失败，请检查日期与权限')}finally{if(v===reportVersion)reportLoading.value=false}}
function downloadReport(){if(!report.value)return;const rows=[['编号','类型','标题','描述','来源','提问人','提问人 UID','负责人','负责人 UID','状态','登记时间'],...report.value.items.map(i=>[i.id,issueKindLabels[i.kind],i.title,i.description,i.scope_name||'私聊',i.reporter_name,i.reporter_uid,i.owner_name,i.owner_uid,issueStatusLabels[i.status],time(i.created_at)])];const url=URL.createObjectURL(new Blob(['\uFEFF'+rows.map(r=>r.map(csvCell).join(',')).join('\r\n')],{type:'text/csv;charset=utf-8'}));const a=document.createElement('a');a.href=url;a.download=`octo-issues-${new Date().toISOString().slice(0,10)}.csv`;a.click();setTimeout(()=>URL.revokeObjectURL(url),1000)}
function invalidate(){listVersion++;metadataVersion++;detailVersion++;contactVersion++;reportVersion++}
watch(()=>auth.currentTenantId,()=>{invalidate();issues.value=[];scopes.value=[];kbs.value=[];contacts.value=[];selected.value=null;report.value=null;detailVisible.value=false;contactVisible.value=false;reportVisible.value=false;filter.value={};contactKB.value='';void metadata();search()})
watch(detailVisible,v=>{if(!v)detailVersion++})
watch(()=>[route.query.view,route.query.kbId],([view,kb])=>{if(['issues','contacts','schedule'].includes(String(view||'')))tab.value=String(view);if(typeof kb==='string'){filter.value.knowledge_base_id=kb;contactKB.value=kb;if(tab.value==='contacts')void loadContacts()}},{immediate:true})
onMounted(()=>{void metadata();void loadIssues()});onBeforeUnmount(invalidate)
</script>
<style scoped>
header h2{font-size:18px;margin:0 0 8px}header p,.hint{font-size:13px;color:var(--td-text-color-secondary);line-height:1.7}.filters{display:grid;grid-template-columns:1.4fr 1.3fr 1.2fr 1fr 1fr;gap:12px;margin:20px 0}.toolbar{display:flex;gap:12px;align-items:center;flex-wrap:wrap;margin:18px 0}.toolbar>.t-select__wrap{min-width:300px}.toolbar label{display:flex;align-items:center;gap:10px;margin:0}input[type=date]{border:1px solid var(--td-component-border);border-radius:4px;padding:7px;color:var(--td-text-color-primary);background:var(--td-bg-color-container)}small{display:block;color:var(--td-text-color-secondary);font-size:12px;margin-top:5px;overflow-wrap:anywhere}label{display:grid;gap:8px;margin:18px 0}.row,.meta{display:flex;gap:12px;align-items:center;flex-wrap:wrap}.meta{color:var(--td-text-color-secondary);font-size:13px}.two-col{display:grid;grid-template-columns:1fr 1fr;gap:16px}dl{display:grid;grid-template-columns:80px 1fr;gap:12px}dd{margin:0;overflow-wrap:anywhere}h4{margin-top:24px}.prewrap{white-space:pre-wrap;overflow-wrap:anywhere;line-height:1.65}blockquote{border-left:3px solid var(--td-component-border);margin:12px 0;padding:10px 16px;background:var(--td-bg-color-secondarycontainer)}.contact-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(280px,1fr));gap:16px}.contact-grid article{padding:18px;border:1px solid var(--td-component-border);border-radius:8px}.history{padding-left:20px}.history li{padding:10px 0}.stat-grid{display:grid;grid-template-columns:repeat(5,1fr);gap:12px;margin:24px 0}.stat-grid strong{font-size:24px}.issue-title{max-width:100%;height:auto;text-align:left;white-space:normal}.t-pagination{margin-top:18px}ul{padding-left:20px}@media(max-width:900px){.filters{grid-template-columns:1fr 1fr}.stat-grid{grid-template-columns:repeat(3,1fr)}}@media(max-width:560px){.filters,.two-col{grid-template-columns:1fr}.toolbar>.t-select__wrap{min-width:0;width:100%}}
</style>
