import { ArrowDown, ArrowUp, Plus, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import type { Configuration, ConfigurationLayers, ConfigurationSource, EffectiveConfiguration, Model } from './schema'

const modules = [
  ['files','文件读写与传输'], ['file-search','文件搜索'], ['skills','Skills 指导'], ['shell','Shell 命令'], ['workers','Worker 委派'], ['collaboration','Agent 间协作'],
  ['mcp','MCP 工具'], ['observations','Observable 观察与唤醒'], ['memory','Memory 读写与学习'], ['calendar','Calendar 日程'],
  ['hooks','Hooks 自动脚本'], ['extensions','扩展资源'], ['notes','Notes 持续工作上下文'], ['tasks','Tasks 任务操作与完成门禁'],
  ['context-control','模型主动压缩与重置上下文'], ['working-files','Thread 持续工作目录'], ['input_tracking','输入确认清单'], ['apply-patch','多文件补丁编辑'], ['chunked-write','分块缓冲写入'],
] as const

export function configurationSource(source?: ConfigurationSource) {
  if (!source) return '尚未读取'
  const names: Record<string,string> = {agent:'Agent',workspace:'Workspace 快照',fleet:'Fleet',tenant:'Tenant',default:'产品默认'}
  const derived:Record<string,string>={files:'文件读写',extensions:'扩展资源'}
  return `${names[source.layer]??source.layer}${source.version?` · v${source.version}`:''}${source.preset?` · ${source.preset==='minimal'?'基础':'标准'}预设`:''}${source.derived_from?` · 跟随${derived[source.derived_from]??source.derived_from}`:''}`
}

export function modelName(models: Model[], id: string) {
  const model=models.find(value=>value.id===id)
  return model?`${model.provider} / ${model.name}`:`不可用模型 · ${id}`
}

export function ConfigurationEditor({value,onChange,models,effective,layers,bottom=false,preview=false}:{value:Configuration;onChange:(value:Configuration)=>void;models:Model[];effective?:EffectiveConfiguration;layers?:ConfigurationLayers;bottom?:boolean;preview?:boolean}) {
  const order=value.models??[]
  const unused=models.filter(model=>!order.includes(model.id))
  function move(index:number,delta:number) {
    const next=[...order]; [next[index],next[index+delta]]=[next[index+delta],next[index]];onChange({...value,models:next})
  }
  return <div className="management-configuration">
    <fieldset className="management-hook-settings"><legend>模型优先级</legend>
      <p>按顺序尝试可用模型。此层设置整条顺序，新 Turn 生效；已开始的 Turn 保留原选择。</p>
      <label className="management-checkbox"><input type="checkbox" checked={value.models==null} onChange={event=>onChange({...value,models:event.target.checked?undefined:models.length?[models[0].id]:[]})}/>{bottom?'暂不设置租户模型':'继承下层模型顺序'}</label>
      {value.models!=null&&<><ol className="management-model-order">{order.map((id,index)=><li key={id}>
        <span>{index+1}</span><select className="management-select" aria-label={`第 ${index+1} 优先模型`} value={id} onChange={event=>onChange({...value,models:order.map((item,i)=>i===index?event.target.value:item)})}>
          {!models.some(model=>model.id===id)&&<option value={id}>{modelName(models,id)}</option>}
          {models.filter(model=>model.id===id||!order.includes(model.id)).map(model=><option key={model.id} value={model.id}>{modelName(models,model.id)}</option>)}
        </select>
        <Button type="button" size="icon" variant="ghost" aria-label={`上移第 ${index+1} 个模型`} disabled={!index} onClick={()=>move(index,-1)}><ArrowUp size={15}/></Button>
        <Button type="button" size="icon" variant="ghost" aria-label={`下移第 ${index+1} 个模型`} disabled={index===order.length-1} onClick={()=>move(index,1)}><ArrowDown size={15}/></Button>
        <Button type="button" size="icon" variant="ghost" aria-label={`移除第 ${index+1} 个模型`} disabled={order.length===1} onClick={()=>onChange({...value,models:order.filter((_,i)=>i!==index)})}><X size={15}/></Button>
      </li>)}</ol><Button type="button" variant="outline" disabled={!unused.length||order.length>=5} onClick={()=>onChange({...value,models:[...order,unused[0].id]})}><Plus size={15}/>添加后备模型</Button>{!order.length&&<p role="alert">请选择至少一个可用模型，或恢复继承。</p>}</>}
      {effective&&<div className="management-effective"><strong>{preview?'应用后预期':'当前生效'} · {configurationSource(effective.model_source)}</strong><p>{effective.models.length?effective.models.map(id=>modelName(models,id)).join(' → '):'尚未选择模型，无法开始新的 Turn。'}</p></div>}
    </fieldset>
    <fieldset className="management-hook-settings"><legend>模块开关</legend><p>逐项继承、开启或关闭。关闭会撤销受影响 Agent 的旧执行授权；重新开启不恢复旧操作。历史数据保留。</p>
<label className="management-field"><span>模块预设</span><select className="management-select" aria-label="模块预设" value={value.module_preset??''} onChange={event=>onChange({...value,module_preset:event.target.value||undefined})}><option value="">{bottom?'产品默认':'继承下层预设'}</option><option value="standard">标准 · 日常工具与工作状态</option><option value="minimal">基础 · 仅文件与 Shell</option></select></label><p>优先级最高的预设提供模块默认值；任意层的显式开关仍优先。独立设置的动态指令不受预设控制。没有预设且尚未单独配置搜索或 Skills 时，它们分别跟随文件、扩展开关。</p>
      <div className="management-module-settings">{modules.map(([id,label])=>{
        const current=effective?.modules[id]
        return <label key={id}><span>{label}{current&&<small>{preview?'应用后':'当前'}{current.enabled?'开启':'关闭'} · {configurationSource(current.source)}</small>}</span><select className="management-select" aria-label={`${label}配置`} value={value.modules?.[id]===undefined?'inherit':value.modules[id]?'on':'off'} onChange={event=>{const next={...value.modules};if(event.target.value==='inherit')delete next[id];else next[id]=event.target.value==='on';onChange({...value,modules:next})}}><option value="inherit">{bottom?`产品默认（${id==='apply-patch'||id==='chunked-write'?'关闭':'开启'}）`:'继承下层'}</option><option value="on">开启</option><option value="off">关闭</option></select></label>
      })}</div><small>Memory 与 Calendar 的应用运行设置由 Fleet 应用管理。文件、Shell、MCP、Observable 在所选执行环境运行；Host Shell 使用所属系统用户权限。</small>
    </fieldset>
    {layers&&<details className="management-config-layers"><summary>查看各层已保存配置与覆盖关系</summary><p>从左到右优先级升高。表格展示服务器上的已保存值，未保存的编辑尚未生效。</p><div className="management-table-wrap"><table className="management-table"><thead><tr><th>配置项</th>{(['tenant','fleet','workspace','agent'] as const).map(layer=><th key={layer}>{configurationSource({layer,version:layers[layer].version})}</th>)}</tr></thead><tbody><tr><th>模块预设</th>{(['tenant','fleet','workspace','agent'] as const).map(layer=><td key={layer}>{layers[layer].declaration.module_preset==='standard'?'标准':layers[layer].declaration.module_preset==='minimal'?'基础':'继承'}</td>)}</tr><tr><th>模型顺序</th>{(['tenant','fleet','workspace','agent'] as const).map(layer=><td key={layer}>{layers[layer].declaration.models?.map(id=>modelName(models,id)).join(' → ')??'继承'}</td>)}</tr>{modules.map(([id,label])=><tr key={id}><th>{label}</th>{(['tenant','fleet','workspace','agent'] as const).map(layer=><td key={layer}>{layers[layer].declaration.modules?.[id]===undefined?'继承':layers[layer].declaration.modules[id]?'开启':'关闭'}</td>)}</tr>)}</tbody></table></div></details>}
  </div>
}
