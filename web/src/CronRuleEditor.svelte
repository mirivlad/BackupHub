<script lang="ts">
 type Field={label:string;min:number;max:number;values:string[];help:string};
 let {value,onChange}:{value:string;onChange:(next:string)=>void}=$props();
 const fields:Field[]=[
  {label:'Минуты',min:0,max:59,values:[],help:'В какие минуты каждого подходящего часа запускать задание'},
  {label:'Часы',min:0,max:23,values:[],help:'В какие часы суток запускать задание'},
  {label:'Числа месяца',min:1,max:31,values:[],help:'В какие числа месяца выполнять копирование'},
  {label:'Месяцы',min:1,max:12,values:['Янв','Фев','Мар','Апр','Май','Июн','Июл','Авг','Сен','Окт','Ноя','Дек'],help:'Выберите один или несколько месяцев'},
  {label:'Дни недели',min:0,max:6,values:['Вс','Пн','Вт','Ср','Чт','Пт','Сб'],help:'Выберите дни недели'}
 ];
 const parts=$derived(value.split(' '));
 function fieldValue(i:number){return parts[i]||'*';}
 function mode(i:number){const v=fieldValue(i);return v==='*'?'any':v.startsWith('*/')?'step':'specific';}
 function summary(i:number){
  const v=fieldValue(i);
  if(v==='*')return 'Любые';
  if(v.startsWith('*/'))return 'Каждые '+v.slice(2);
  const values=v.split(',').map(n=>Number(n));
  if(i===3||i===4)return values.map(n=>fields[i].values[n-fields[i].min]??n).join(', ');
  return values.join(', ');
 }
 function update(i:number,v:string){
  const next=[...parts];while(next.length<5)next.push('*');next[i]=v;onChange(next.join(' '));
 }
 function updateMode(i:number,m:string){
  if(m==='any')update(i,'*');
  else if(m==='step')update(i,'*/2');
  else update(i,String(i===0?0:i===1?2:i===4?1:1));
 }
 function toggle(i:number,n:number){
  const current=fieldValue(i).split(',').map(Number).filter(x=>Number.isInteger(x)&&x>=fields[i].min&&x<=fields[i].max);
  const next=current.includes(n)?current.filter(x=>x!==n):[...current,n];
  if(next.length)update(i,next.sort((a,b)=>a-b).join(','));
 }
 const selections=$derived(fields.map(f=>Array.from({length:f.max-f.min+1},(_,i)=>f.min+i)));
</script>

<div class="cron-rules">
 <p class="small-intro">Гибкий конструктор: выберите нужные минуты, часы, числа, месяцы и дни недели. Строку cron вводить не нужно.</p>
 {#each fields as field,i}
  <details class="cron-card">
   <summary><strong>{field.label}</strong><span>{summary(i)}</span></summary>
   <div class="field-body">
    <p>{field.help}</p>
    <div class="mode-select">
     <label><input type="radio" name={'cron-mode-'+i} checked={mode(i)==='any'} onchange={()=>updateMode(i,'any')}/>Любые</label>
     <label><input type="radio" name={'cron-mode-'+i} checked={mode(i)==='specific'} onchange={()=>updateMode(i,'specific')}/>Выбранные значения</label>
     <label><input type="radio" name={'cron-mode-'+i} checked={mode(i)==='step'} onchange={()=>updateMode(i,'step')}/>С шагом</label>
    </div>
    {#if mode(i)==='step'}
     <label class="step-label">Каждые
      <input type="number" min="1" max={field.max-field.min+1} value={Number(fieldValue(i).slice(2))||2} onchange={(e)=>update(i,'*/'+Math.max(1,Math.min(field.max-field.min+1,Number(e.currentTarget.value)||1)))}/>
      {i===0?'минут':i===1?'часов':i===2?'дней месяца':i===3?'месяцев':'дней недели'}
     </label>
    {:else if mode(i)==='specific'}
     <div class="value-grid">
      {#each selections[i] as n(n)}
       <button type="button" class:selected={fieldValue(i).split(',').includes(String(n))} aria-pressed={fieldValue(i).split(',').includes(String(n))}
        onclick={()=>toggle(i,n)}>{field.values[n-field.min]??String(n).padStart(2,'0')}</button>
      {/each}
     </div>
    {/if}
   </div>
  </details>
 {/each}
 <p class="cron-note">Если выбраны одновременно числа месяца и дни недели, задание срабатывает при совпадении <strong>любого</strong> из этих условий — как в обычном cron. Часовой пояс задаётся в форме задания.</p>
</div>
<style>
 .cron-rules{display:grid;gap:9px;grid-column:1/-1}
 .small-intro,.cron-note,.field-body p{color:var(--muted);font-size:12px;margin:0 0 6px;line-height:1.5}
 .cron-card{border:1px solid var(--line);border-radius:10px;background:#fcfdff;overflow:hidden}
 .cron-card summary{list-style:none;padding:13px 15px;cursor:pointer;display:flex;justify-content:space-between;gap:15px;align-items:center}
 .cron-card summary::-webkit-details-marker{display:none}
 .cron-card summary::before{content:'▸';color:#8191a5;margin-right:3px}
 .cron-card[open] summary::before{content:'▾'}
 .cron-card summary strong{font-size:13px;flex:1}
 .cron-card summary span{font-size:12px;color:#4775b0;text-align:right;overflow-wrap:anywhere}
 .field-body{padding:15px;border-top:1px solid var(--line)}
 .mode-select{display:flex;gap:15px;flex-wrap:wrap;margin:12px 0}
 .mode-select label{display:flex;align-items:center;gap:6px;font-size:12px;cursor:pointer}
 .mode-select input{accent-color:var(--primary)}
 .value-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(48px,1fr));gap:6px;margin-top:12px}
 .value-grid button{background:#fff;border:1px solid var(--line);border-radius:7px;padding:9px 2px;color:var(--text);font-size:12px}
 .value-grid button.selected{background:#e7f0ff;border-color:#6e9ce1;color:#1b5aab;font-weight:700}
 .step-label{display:flex;align-items:center;gap:10px;font-size:13px;margin-top:10px}
 .step-label input{width:88px;max-width:88px}
 .cron-note{margin-top:8px}
</style>
