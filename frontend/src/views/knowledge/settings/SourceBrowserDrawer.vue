<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import SettingDrawer from '@/components/settings/SettingDrawer.vue'
import { listSourceSnapshots, sourceTree, sourceRead, sourceSearch, type SourceSummary, type SourceEntry, type SourceRead, type SourceMatch } from '@/api/source-snapshot'
const props=defineProps<{kbId:string;initialSourceId?:string;initialPath?:string;initialSnapshotId?:string;initialLine?:number}>()
const visible=defineModel<boolean>('visible',{default:false})
const {t}=useI18n()
const sources=ref<SourceSummary[]>([]), selected=ref(''), snapshot=ref(''), directory=ref(''), query=ref(''), error=ref('')
const entries=ref<SourceEntry[]>([]), matches=ref<SourceMatch[]>([]), content=ref<SourceRead|null>(null)
const loading=ref(false), searched=ref(false), complete=ref(true), offset=ref(0), total=ref(0)
let requestId=0
const current=computed(()=>sources.value.find(s=>s.id===selected.value))
const skipped=computed(()=>Object.values(current.value?.skipped||{}).reduce((sum,n)=>sum+n,0))
const lines=computed(()=>content.value?.content.split('\n')||[])
const remoteURL=computed(()=>content.value?.source_url?.startsWith('https://github.com/')?content.value.source_url:'')
async function loadTree(){
 const id=++requestId;loading.value=true;error.value='';searched.value=false;content.value=null
 try{const value=await sourceTree(props.kbId,selected.value,snapshot.value,directory.value,offset.value);if(id!==requestId)return;snapshot.value=value.snapshot_id;entries.value=value.entries;total.value=value.total}catch(e:any){if(id===requestId)error.value=e?.message||t('datasource.source.unavailable')}finally{if(id===requestId)loading.value=false}
}
async function selectSource(){snapshot.value='';directory.value='';offset.value=0;query.value='';await loadTree()}
async function openFile(path:string,line=1){
 const id=++requestId;loading.value=true;error.value=''
 try{const value=await sourceRead(props.kbId,selected.value,snapshot.value,path,line);if(id===requestId){content.value=value;snapshot.value=value.snapshot_id}}catch(e:any){if(id===requestId)error.value=e?.message||t('datasource.source.unavailable')}finally{if(id===requestId)loading.value=false}
}
async function search(){
 if(!query.value.trim())return loadTree()
 const id=++requestId;loading.value=true;error.value='';content.value=null
 try{const value=await sourceSearch(props.kbId,selected.value,snapshot.value,query.value,directory.value);if(id!==requestId)return;matches.value=value.matches;snapshot.value=value.snapshot_id;complete.value=value.complete;searched.value=true}catch(e:any){if(id===requestId)error.value=e?.message||t('datasource.source.unavailable')}finally{if(id===requestId)loading.value=false}
}
async function enter(entry:SourceEntry){if(entry.directory){directory.value=entry.path;offset.value=0;await loadTree()}else await openFile(entry.path)}
function up(){directory.value=directory.value.split('/').slice(0,-1).join('/');offset.value=0;void loadTree()}
watch(()=>[visible.value,props.kbId,props.initialSourceId,props.initialPath,props.initialSnapshotId,props.initialLine],async ()=>{
 const opened=visible.value
 ++requestId;if(!opened)return
 sources.value=[];entries.value=[];content.value=null;error.value='';directory.value='';offset.value=0;query.value='';snapshot.value='';loading.value=true
 const id=requestId
 try{const value=await listSourceSnapshots(props.kbId);if(id!==requestId)return;sources.value=value;selected.value=props.initialSourceId||value[0]?.id||''
  if(!value.some(s=>s.id===selected.value)){error.value=t('datasource.source.noSources');return}
  snapshot.value=props.initialSnapshotId||'';await loadTree();if(props.initialPath&&!error.value)await openFile(props.initialPath,props.initialLine||1)
 }catch(e:any){error.value=e?.message||t('datasource.source.unavailable')}finally{loading.value=false}
},{immediate:true})
</script>
<template>
 <SettingDrawer v-model:visible="visible" :title="t('datasource.source.browse')" width="1100px" resizable hide-footer>
  <t-alert v-if="error" theme="warning" :message="error" />
  <div class="source-controls">
   <t-select v-model="selected" @change="selectSource"><t-option v-for="source in sources" :key="source.id" :value="source.id" :label="source.name" /></t-select>
   <t-input v-model="query" :placeholder="t('datasource.source.searchHint')" @enter="search" clearable />
   <t-button :loading="loading" @click="search">{{ t('datasource.source.search') }}</t-button>
  </div>
  <p class="source-caption">{{ t('datasource.source.readonly') }} · {{ current?.file_count||0 }} {{ t('datasource.source.files') }} <span v-if="snapshot">· {{ snapshot.slice(0,12) }}</span></p>
  <t-alert v-if="skipped" theme="info" :message="t('datasource.source.skipped',{count:skipped})" />
  <div v-if="selected" class="source-layout">
   <aside class="source-tree">
    <div class="source-directory"><t-button v-if="directory" variant="text" size="small" @click="up">↑</t-button><span>{{ directory||'/' }}</span></div>
    <template v-if="searched">
     <p v-if="!complete" class="source-caption">{{ t('datasource.source.partial') }}</p>
     <button v-for="(match,index) in matches" :key="index" class="source-entry" @click="openFile(match.path,Math.max(1,match.line-3))"><span>{{ match.path }}:{{ match.line }}</span><small>{{ match.text }}</small></button>
     <p v-if="!matches.length&&!loading">{{ t('datasource.source.noMatches') }}</p>
    </template>
    <template v-else>
     <button v-for="entry in entries" :key="entry.path" class="source-entry" @click="enter(entry)"><t-icon :name="entry.directory?'folder':'code'"/><span>{{ entry.path.split('/').pop() }}</span></button>
     <div v-if="total>200" class="source-pages"><t-button :disabled="offset===0" size="small" @click="offset-=200;loadTree()">←</t-button><span>{{ offset+1 }} / {{ total }}</span><t-button :disabled="offset+200>=total" size="small" @click="offset+=200;loadTree()">→</t-button></div>
    </template>
   </aside>
   <main class="source-content">
    <template v-if="content">
     <div class="source-file-heading"><strong>{{ content.path }}</strong><t-link :href="content.preview_url" target="_blank" rel="noopener noreferrer">{{ t('datasource.source.platformLink') }}</t-link><t-link v-if="remoteURL" :href="remoteURL" target="_blank" rel="noopener noreferrer">{{ t('datasource.source.origin') }}</t-link></div>
     <p class="source-caption">{{ content.start_line }}–{{ content.end_line }} / {{ content.total_lines }} · {{ content.revision.slice(0,24) }}</p>
     <p v-if="!remoteURL" class="source-caption">{{ t('datasource.source.localCitation') }}</p>
     <p v-if="content.truncated" class="source-caption">{{ t('datasource.source.partialRead') }}</p>
     <div class="source-code"><div v-for="(line,index) in lines" :key="index" class="source-line"><span>{{ content.start_line+index }}</span><code>{{ line }}</code></div></div>
     <div class="source-pages"><t-button :disabled="content.start_line<=1" size="small" @click="openFile(content.path,Math.max(1,content.start_line-100))">←</t-button><t-button :disabled="content.end_line>=content.total_lines" size="small" @click="openFile(content.path,content.end_line+1)">→</t-button></div>
    </template>
    <p v-else class="source-caption">{{ t('datasource.source.selectFile') }}</p>
   </main>
  </div>
 </SettingDrawer>
</template>
<style scoped>
.source-controls{display:flex;gap:10px;align-items:center;margin-bottom:12px}.source-controls>:first-child{max-width:240px}.source-caption{font-size:12px;color:var(--td-text-color-secondary);line-height:1.6}.source-layout{display:grid;grid-template-columns:260px minmax(0,1fr);border:1px solid var(--td-component-stroke);border-radius:8px;min-height:400px;margin-top:16px}.source-tree{border-right:1px solid var(--td-component-stroke);max-height:65vh;overflow:auto;padding:10px}.source-content{padding:16px;min-width:0;overflow:auto;max-height:65vh}.source-directory{display:flex;align-items:center;gap:6px;overflow-wrap:anywhere;font-size:12px;margin-bottom:8px}.source-entry{display:flex;gap:8px;align-items:center;flex-wrap:wrap;width:100%;border:0;background:transparent;color:var(--td-text-color-primary);padding:9px 8px;text-align:left;cursor:pointer;border-radius:4px;overflow-wrap:anywhere}.source-entry:hover{background:var(--td-bg-color-container-hover)}.source-entry small{width:100%;overflow:hidden;white-space:nowrap;text-overflow:ellipsis;color:var(--td-text-color-secondary)}.source-file-heading,.source-pages{display:flex;justify-content:space-between;gap:10px;align-items:center;margin-bottom:10px}.source-pages{margin-top:12px}.source-code{overflow:auto;background:var(--td-bg-color-secondarycontainer);border-radius:6px;padding:12px 0;font-size:12px}.source-line{display:flex;min-width:max-content;line-height:1.7}.source-line>span{min-width:48px;padding:0 12px;text-align:right;color:var(--td-text-color-placeholder);user-select:none}.source-line code{white-space:pre;font-family:ui-monospace,SFMono-Regular,Consolas,monospace;padding-right:14px}@media(max-width:760px){.source-layout{grid-template-columns:1fr}.source-tree{max-height:180px;border-right:0;border-bottom:1px solid var(--td-component-stroke)}.source-controls{flex-wrap:wrap}}
</style>
