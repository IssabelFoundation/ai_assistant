(function(){
  'use strict';
  // Small Markdown subset built only with DOM nodes: response HTML stays literal.
  function appendInline(parent,text){
    var tokens=/(`[^`\n]+`|\*\*[^*\n]+\*\*|\*[^*\n]+\*)/g,match,offset=0;
    while((match=tokens.exec(text))){
      parent.appendChild(document.createTextNode(text.slice(offset,match.index)));
      var token=match[0],tag=token.charAt(0)==='`'?'code':token.slice(0,2)==='**'?'strong':'em';
      var size=tag==='strong'?2:1,node=document.createElement(tag);
      node.textContent=token.slice(size,-size);parent.appendChild(node);offset=tokens.lastIndex;
    }
    parent.appendChild(document.createTextNode(text.slice(offset)));
  }
  function tableCells(line){
    var cells=[],cell='',hasPipe=false;
    line=line.trim();
    for(var i=0;i<line.length;i++){
      if(line.charAt(i)==='\\'&&i+1<line.length){
        var next=line.charAt(++i);cell+=next==='|'?'|':'\\'+next;
      }else if(line.charAt(i)==='|'){cells.push(cell.trim());cell='';hasPipe=true;}
      else cell+=line.charAt(i);
    }
    cells.push(cell.trim());
    if(line.charAt(0)==='|')cells.shift();
    if(hasPipe&&cells[cells.length-1]===''&&line.charAt(line.length-1)==='|')cells.pop();
    return hasPipe?cells:null;
  }
  function appendTableRow(section,cells,alignments,header){
    var row=document.createElement('tr');
    alignments.forEach(function(alignment,index){
      var cell=document.createElement(header?'th':'td');
      if(header)cell.setAttribute('scope','col');
      cell.style.textAlign=alignment;appendInline(cell,cells[index]||'');row.appendChild(cell);
    });
    section.appendChild(row);
  }
  function renderAssistant(item,text){
    item.textContent='';
    var lines=String(text).replace(/\r\n?/g,'\n').split('\n'),paragraph=null,list=null,code=null,skipUntil=0;
    lines.forEach(function(line,index){
      if(index<skipUntil)return;
      if(/^\s*```/.test(line)){
        paragraph=null;list=null;
        if(code){code=null;}else{var pre=document.createElement('pre');code=document.createElement('code');pre.appendChild(code);item.appendChild(pre);}
        return;
      }
      if(code){code.appendChild(document.createTextNode(line+'\n'));return;}
      if(!line.trim()){paragraph=null;list=null;return;}
      var headers=tableCells(line),separator=index+1<lines.length?tableCells(lines[index+1]):null;
      if(headers&&separator&&headers.length===separator.length&&separator.every(function(cell){return /^:?-{3,}:?$/.test(cell);})){ 
        paragraph=null;list=null;
        var alignments=separator.map(function(cell){return cell.charAt(cell.length-1)===':'?(cell.charAt(0)===':'?'center':'right'):'left';});
        var wrapper=document.createElement('div'),table=document.createElement('table'),head=document.createElement('thead'),body=document.createElement('tbody');
        wrapper.className='ai-table-scroll';wrapper.setAttribute('tabindex','0');wrapper.setAttribute('role','region');wrapper.setAttribute('aria-label','Tabla de la respuesta');
        appendTableRow(head,headers,alignments,true);table.appendChild(head);table.appendChild(body);wrapper.appendChild(table);item.appendChild(wrapper);
        skipUntil=index+2;
        while(skipUntil<lines.length){
          var rowCells=tableCells(lines[skipUntil]);
          if(!rowCells||/^\s*```/.test(lines[skipUntil]))break;
          appendTableRow(body,rowCells,alignments,false);skipUntil++;
        }
        return;
      }
      var entry=/^\s*(?:([-+*])|\d+[.)])\s+(.+)$/.exec(line);
      if(entry){
        paragraph=null;var tag=entry[1]?'ul':'ol';
        if(!list||list.tagName.toLowerCase()!==tag){list=document.createElement(tag);item.appendChild(list);}
        var li=document.createElement('li');appendInline(li,entry[2]);list.appendChild(li);return;
      }
      list=null;
      if(!paragraph){paragraph=document.createElement('p');item.appendChild(paragraph);}else{paragraph.appendChild(document.createElement('br'));}
      appendInline(paragraph,line);
    });
  }
  function init(){
  var root=document.querySelector('.issabel-ai');
  if(!root)return;
  var csrf=root.getAttribute('data-csrf'), conversationId='', messages=document.getElementById('messages'), plans=document.getElementById('plans'), activity=document.getElementById('activity');
  var apiBase='/modules/issabel_ai_assistant/index.php?api=';
  var dialogQueue=Promise.resolve();
  function showDialog(options){
    var result=dialogQueue.then(function(){return new Promise(function(resolve){
      var previous=document.activeElement,dialog=document.createElement('dialog');
      dialog.className='ai-dialog';dialog.setAttribute('aria-labelledby','ai-dialog-title');
      var heading=document.createElement('h2');heading.id='ai-dialog-title';heading.textContent=options.title||'Asistente IA';dialog.appendChild(heading);
      var content=document.createElement(options.json?'pre':'p');content.className='ai-dialog-content';content.textContent=options.text||'';dialog.appendChild(content);
      var input=null;
      if(options.input){
        var label=document.createElement('label');label.textContent=options.inputLabel||'Valor';
        input=document.createElement('input');input.type='password';input.autocomplete='new-password';label.appendChild(input);dialog.appendChild(label);
      }
      var status=document.createElement('p');status.className='ai-dialog-status';status.setAttribute('role','status');dialog.appendChild(status);
      var actions=document.createElement('div');actions.className='ai-actions';dialog.appendChild(actions);
      if(options.json){
        var copy=document.createElement('button');copy.type='button';copy.className='ai-secondary';copy.textContent='Copiar contenido';
        copy.onclick=function(){
          if(!navigator.clipboard){status.textContent='Seleccioná el texto para copiarlo manualmente.';return;}
          navigator.clipboard.writeText(options.text||'').then(function(){status.textContent='Contenido copiado.'}).catch(function(){status.textContent='No se pudo copiar. Podés seleccionar el texto manualmente.'});
        };actions.appendChild(copy);
      }
      var finished=false;
      function finish(value){
        if(finished)return;finished=true;
        if(input)input.value='';
        dialog.close();dialog.remove();
        if(previous&&previous.isConnected)previous.focus();resolve(value);
      }
      var cancel=null;
      if(options.confirm||options.input){
        cancel=document.createElement('button');cancel.type='button';cancel.className='ai-secondary';cancel.textContent='Volver';cancel.onclick=function(){finish(options.input?null:false)};actions.appendChild(cancel);
      }
      var accept=document.createElement('button');accept.type='button';accept.textContent=options.accept||'Cerrar';
      if(options.danger)accept.className='ai-danger';
      accept.onclick=function(){finish(input?input.value:true)};actions.appendChild(accept);
      dialog.addEventListener('cancel',function(event){event.preventDefault();finish(options.input?null:false)});
      root.appendChild(dialog);dialog.showModal();
      (input||cancel||accept).focus();
    })});
    dialogQueue=result.catch(function(){});return result;
  }
  function showError(error){return showDialog({title:'No se pudo completar la operación',text:error.message||String(error)})}

  function api(name,options){options=options||{};options.headers=options.headers||{};options.headers['X-CSRF-Token']=csrf;if(options.body)options.headers['Content-Type']='application/json';return fetch(apiBase+name+(options.query||''),options).then(function(r){if(options.raw)return r;return r.text().then(function(t){var d={};try{d=t?JSON.parse(t):{}}catch(e){d={detail:t}}if(!r.ok)throw new Error(d.detail||'Error HTTP '+r.status);return d;});});}
  function setBusy(text){activity.textContent=text||'';}
  function addMessage(role,text,kind){var item=document.createElement('article');item.className=kind==='plan_result'?'ai-system':role;if(kind==='plan_result')text='Sistema: '+text;if(role==='assistant')renderAssistant(item,text);else item.textContent=text;messages.appendChild(item);messages.scrollTop=messages.scrollHeight;return item;}
  function loadModels(){return api('models').then(function(d){var list=document.getElementById('modelOptions');list.textContent='';(d.models||[]).forEach(function(name){var option=document.createElement('option');option.value=name;list.appendChild(option)})})}
  function streamChat(payload,onDelta){return fetch(apiBase+'chat_stream',{method:'POST',headers:{'X-CSRF-Token':csrf,'Content-Type':'application/json'},body:JSON.stringify(payload)}).then(function(r){if(!r.ok)throw new Error('Error HTTP '+r.status);if(!r.body||!r.body.getReader)return r.text().then(function(text){var match=/event: complete\ndata: (.+)/.exec(text);if(!match)throw new Error('Respuesta incompleta');return JSON.parse(match[1])});var reader=r.body.getReader(),decoder=new TextDecoder(),buffer='';return new Promise(function(resolve,reject){function read(){reader.read().then(function(part){buffer+=decoder.decode(part.value||new Uint8Array(),{stream:!part.done});var blocks=buffer.split('\n\n');buffer=blocks.pop();blocks.forEach(function(block){var lines=block.split('\n'),event='',data='';lines.forEach(function(line){if(line.indexOf('event:')===0)event=line.slice(6).trim();if(line.indexOf('data:')===0)data+=line.slice(5).trim()});if(event==='status')setBusy('Pensando…');if(event==='delta'){try{onDelta(JSON.parse(data).text||'')}catch(e){reject(e)}}if(event==='error'){try{reject(new Error(JSON.parse(data).error))}catch(e){reject(e)}}if(event==='complete'){try{resolve(JSON.parse(data))}catch(e){reject(e)}}});if(part.done){if(buffer)reject(new Error('Respuesta incompleta'));return}read()}).catch(reject)}read()})})}
  function showPlan(plan){var summary=plan.summary||plan;var card=document.createElement('article');card.className='ai-plan';var title=document.createElement('h3');title.textContent='Plan pendiente de aprobación';card.appendChild(title);var dl=document.createElement('dl'),statusCell=null,rows;if(summary.operation==='create_queue'){rows=[['ID',plan.plan_id],['Operación','Crear cola'],['Extensión',summary.extension],['Nombre',summary.name],['Estrategia',summary.strategy],['Agentes estáticos',Object.keys(summary.static_agents||{}).join(', ')||'Ninguno'],['Agentes dinámicos',Object.keys(summary.dynamic_agents||{}).join(', ')||'Ninguno'],['Espera máxima',summary.max_wait_seconds+' s'],['Timeout por agente',summary.agent_timeout_seconds+' s'],['Failover',summary.failover+(summary.failover_extension?' '+summary.failover_extension:'')],['Estado',plan.status]]}else if(summary.operation==='delete_queue'){rows=[['ID',plan.plan_id],['Operación','Eliminar cola'],['Extensión',summary.extension],['Nombre',summary.name],['Estado',plan.status]]}else if(summary.operation==='create_ringgroup'){rows=[['ID',plan.plan_id],['Operación','Crear grupo de timbrado'],['Extensión',summary.extension],['Nombre',summary.name],['Miembros',(summary.members||[]).join(', ')],['Estrategia',summary.strategy],['Tiempo de timbrado',summary.ring_time_seconds+' s'],['Failover',summary.failover+(summary.failover_extension?' '+summary.failover_extension:'')],['Estado',plan.status]]}else if(summary.operation==='update_ringgroup'){rows=[['ID',plan.plan_id],['Operación','Actualizar grupo de timbrado'],['Extensión',summary.extension],['Nombre actual',summary.name],['Campos a cambiar',(summary.changed_fields||[]).join(', ')],['Cambios',JSON.stringify(summary.changes)],['Estado',plan.status]]}else if(summary.operation==='delete_ringgroup'){rows=[['ID',plan.plan_id],['Operación','Eliminar grupo de timbrado'],['Extensión',summary.extension],['Nombre',summary.name],['Estado',plan.status]]}else if(summary.operation==='create_time_group'){rows=[['ID',plan.plan_id],['Operación','Crear grupo horario'],['Nombre',summary.name],['Rangos',(summary.stored_times||[]).join(' · ')],['Estado',plan.status]]}else if(summary.operation==='delete_time_group'){rows=[['ID',plan.plan_id],['Operación','Eliminar grupo horario'],['Id',summary.id],['Nombre',summary.name],['Estado',plan.status]]}else if(summary.operation==='create_time_condition'){rows=[['ID',plan.plan_id],['Operación','Crear condición horaria'],['Nombre',summary.name],['Grupo horario',summary.time_group_id+(summary.time_group_name?' ('+summary.time_group_name+')':'')],['Si coincide',summary.matches.type+(summary.matches.destination_extension?' '+summary.matches.destination_extension:'')],['Si no coincide',summary.does_not_match.type+(summary.does_not_match.destination_extension?' '+summary.does_not_match.destination_extension:'')],['Estado',plan.status]]}else if(summary.operation==='delete_time_condition'){rows=[['ID',plan.plan_id],['Operación','Eliminar condición horaria'],['Id',summary.id],['Nombre',summary.name],['Estado',plan.status]]}else if(summary.operation==='create_ivr'){rows=[['ID',plan.plan_id],['Operación','Crear IVR'],['Nombre',summary.name],['Timeout',summary.timeout_seconds+' s'],['Si no hay respuesta',function(x){return x.type+(x.destination_extension?' '+x.destination_extension:'')}(summary.timeout_destination)],['Si la opción no existe',function(x){return x.type+(x.destination_extension?' '+x.destination_extension:'')}(summary.invalid_destination)],['Opciones',(summary.entries||[]).map(function(e){return e.digits+' → '+function(x){return x.type+(x.destination_extension?' '+x.destination_extension:'')}(e.destination)}).join(' · ')||'Ninguna'],['Estado',plan.status]]}else if(summary.operation==='update_ivr'){rows=[['ID',plan.plan_id],['Operación','Actualizar IVR'],['Id',summary.id],['Nombre actual',summary.name],['Campos a cambiar',(summary.changed_fields||[]).join(', ')],['Cambios',JSON.stringify(summary.changes)],['Estado',plan.status]]}else if(summary.operation==='delete_ivr'){rows=[['ID',plan.plan_id],['Operación','Eliminar IVR'],['Id',summary.id],['Nombre',summary.name],['Estado',plan.status]]}else{rows=[['ID',plan.plan_id],['Operación',summary.operation],['Perfil',summary.profile||'—'],['Extensiones',(summary.extensions||[]).join(', ')],['Voicemail',summary.voicemail===true?'Activado':summary.voicemail===false?'Desactivado':'—'],['Estado',plan.status]]}rows.forEach(function(row){var dt=document.createElement('dt'),dd=document.createElement('dd');dt.textContent=row[0];dd.textContent=row[1];if(row[0]==='Estado')statusCell=dd;dl.appendChild(dt);dl.appendChild(dd);});card.appendChild(dl);var review=document.createElement('button');review.className='ai-secondary';review.textContent='Revisar';review.onclick=function(){showDialog({title:'Revisar plan',text:JSON.stringify(summary,null,2),json:true})};var audit=document.createElement('button');audit.className='ai-secondary';audit.textContent='Auditoría';audit.onclick=function(){api('plan_audit',{query:'&id='+encodeURIComponent(plan.plan_id)}).then(function(d){showDialog({title:'Auditoría del plan '+plan.plan_id,text:JSON.stringify(d.events||[],null,2),json:true})}).catch(function(e){showError(e)})};var approve=document.createElement('button');approve.textContent='Aprobar y ejecutar';approve.onclick=async function(){if(approve.disabled)return;approve.disabled=true;if(!await showDialog({title:'Aprobar y ejecutar plan',text:'Revisá el plan '+plan.plan_id+'. ¿Aprobar y ejecutar esta modificación en la PBX?',confirm:true,accept:'Aprobar y ejecutar'})){approve.disabled=false;return;}var execution={};if(summary.password_mode==='provided_at_execution'){execution.passwords={};for(var i=0;i<summary.extensions.length;i++){var ext=summary.extensions[i],password=await showDialog({title:'Contraseña de extensión',text:'Ingresá una contraseña de al menos 16 caracteres para '+ext+'.',input:true,inputLabel:'Contraseña',accept:'Continuar'});if(password===null){approve.disabled=false;return;}execution.passwords[ext]=password}}if(summary.voicemail_pin_mode==='provided_at_execution'){execution.voicemail_pins={};for(var j=0;j<summary.extensions.length;j++){var vmext=summary.extensions[j],pin=await showDialog({title:'PIN de voicemail',text:'Ingresá el PIN de voicemail para '+vmext+'.',input:true,inputLabel:'PIN',accept:'Continuar'});if(pin===null){approve.disabled=false;return;}execution.voicemail_pins[vmext]=pin}}var outcomeShown=false,executionConfirmed=false,executionFailed=false;approve.disabled=true;cancel.disabled=true;setBusy('Ejecutando…');api('approve_execute',{method:'POST',query:'&id='+encodeURIComponent(plan.plan_id),body:JSON.stringify(execution),raw:true}).then(function(r){var event=r.headers.get('X-Issabel-Plan-Message');if(event){outcomeShown=true;addMessage('assistant',decodeURIComponent(event),'plan_result');if(r.headers.get('X-Issabel-Plan-History')!=='saved')addMessage('assistant','El resultado se recibió, pero no pudo guardarse en el historial. Revisá la auditoría del plan.','plan_result')}if(!r.ok){return r.text().then(function(text){var result={};try{result=JSON.parse(text)}catch(ignored){}if(result.status==='failed'&&result.plan_id===plan.plan_id){executionFailed=true;plan.status='failed';title.textContent='Plan fallido';if(statusCell)statusCell.textContent='Fallido';approve.textContent='Fallido';approve.disabled=true;cancel.disabled=true;}if(!event){outcomeShown=true;addMessage('assistant',executionFailed?'La ejecución del plan '+plan.plan_id+' falló. Revisá la auditoría para conocer el motivo.':'No se pudo confirmar el resultado del plan '+plan.plan_id+'. Revisá su auditoría antes de reintentar.','plan_result');}throw new Error(executionFailed?'El plan falló. Revisá su auditoría; este plan ya no puede ejecutarse ni cancelarse.':'No se pudo completar el plan; revisá su auditoría.');})}if(!event)addMessage('assistant','El plan '+plan.plan_id+' ha sido ejecutado con éxito. El servidor no confirmó su registro en el historial.','plan_result');executionConfirmed=true;title.textContent='Plan ejecutado';if(statusCell)statusCell.textContent='Ejecutado';approve.disabled=true;approve.textContent='Ejecutado';cancel.disabled=true;var type=r.headers.get('Content-Type')||'';if(type.indexOf('text/csv')<0){approve.disabled=true;approve.textContent='Ejecutado';return r.json()}return r.blob().then(function(blob){var url=URL.createObjectURL(blob),a=document.createElement('a');a.href=url;a.download='issabel-extension-credentials-'+plan.plan_id+'.csv';a.click();setTimeout(function(){URL.revokeObjectURL(url)},1000);approve.disabled=true;approve.textContent='Ejecutado';});}).catch(function(e){if(executionConfirmed)addMessage('assistant','El plan se ejecutó, pero no se pudo procesar o descargar su respuesta.','plan_result');else if(!outcomeShown)addMessage('assistant','No se pudo confirmar el resultado del plan '+plan.plan_id+'. Revisá su estado y auditoría antes de reintentar.','plan_result');showError(e)}).finally(function(){if(!executionConfirmed&&!executionFailed){approve.disabled=false;cancel.disabled=false;}setBusy('')});};var cancel=document.createElement('button');cancel.className='ai-danger';cancel.textContent='Cancelar';cancel.onclick=async function(){if(!await showDialog({title:'Cancelar plan',text:'¿Cancelar el plan '+plan.plan_id+'?',confirm:true,danger:true,accept:'Cancelar plan'}))return;api('cancel_plan',{method:'POST',query:'&id='+encodeURIComponent(plan.plan_id),body:'{}'}).then(function(){card.remove()}).catch(function(e){showError(e)})};var actions=document.createElement('div');actions.className='ai-actions';actions.appendChild(review);actions.appendChild(audit);actions.appendChild(approve);actions.appendChild(cancel);card.appendChild(actions);plans.appendChild(card);}
  document.getElementById('settingsButton').onclick=function(){document.getElementById('settings').classList.toggle('ai-hidden')};
  document.getElementById('pendingButton').onclick=function(){setBusy('Cargando planes…');api('plans').then(function(d){plans.textContent='';(d.results||[]).filter(function(p){return p.status==='pending_approval'||p.status==='approved'}).forEach(function(p){p.plan_id=p.id;showPlan(p)});if(!plans.firstChild)addMessage('assistant','No hay planes pendientes.')}).catch(function(e){addMessage('assistant','No se pudieron consultar los planes: '+e.message)}).finally(function(){setBusy('')})};
  document.getElementById('saveProvider').onclick=function(){var body={provider:document.getElementById('provider').value,model:document.getElementById('model').value,api_key:document.getElementById('apiKey').value};api('provider',{method:'PUT',body:JSON.stringify(body)}).then(function(d){document.getElementById('apiKey').value='';document.getElementById('providerStatus').textContent='Clave guardada ••••'+(d.key_suffix||'');return loadModels()}).catch(function(e){document.getElementById('providerStatus').textContent=e.message})};
  document.getElementById('testProvider').onclick=function(){setBusy('Probando…');api('provider_test',{method:'POST',body:'{}'}).then(function(d){document.getElementById('providerStatus').textContent='Conexión correcta ('+d.models_found+' modelos)'}).catch(function(e){document.getElementById('providerStatus').textContent=e.message}).finally(function(){setBusy('')})};
  document.getElementById('deleteProvider').onclick=async function(){if(!await showDialog({title:'Borrar clave',text:'¿Borrar la clave del proveedor guardada para tu usuario?',confirm:true,danger:true,accept:'Borrar clave'}))return;api('provider',{method:'DELETE'}).then(function(){document.getElementById('providerStatus').textContent='Clave eliminada'}).catch(showError)};
  document.getElementById('chatForm').onsubmit=function(e){e.preventDefault();var input=document.getElementById('message'),text=input.value.trim();if(!text)return;addMessage('user',text);input.value='';setBusy('Conectando…');var draft=null,draftText='';streamChat({conversation_id:conversationId,message:text},function(chunk){if(!draft)draft=addMessage('assistant','');draftText+=chunk;renderAssistant(draft,draftText);messages.scrollTop=messages.scrollHeight}).then(function(d){conversationId=d.conversation_id;if(!draft)draft=addMessage('assistant',d.message||'Plan preparado.');else renderAssistant(draft,d.message||draftText);(d.plans||[]).forEach(showPlan)}).catch(function(e){if(draft)draft.textContent='Error: '+e.message;else addMessage('assistant','Error: '+e.message)}).finally(function(){setBusy('')})};
  document.getElementById('historyButton').onclick=function(){var panel=document.getElementById('history');panel.classList.toggle('ai-hidden');if(panel.classList.contains('ai-hidden'))return;api('conversations').then(function(d){var list=document.getElementById('historyList');list.textContent='';(d.conversations||[]).forEach(function(c){var row=document.createElement('p'),open=document.createElement('button');open.className='ai-secondary';open.textContent=c.title||c.id;open.onclick=function(){api('conversation',{query:'&id='+encodeURIComponent(c.id)}).then(function(full){conversationId=full.id;messages.textContent='';(full.messages||[]).forEach(function(m){addMessage(m.role,m.content,m.kind)})})};var del=document.createElement('button');del.className='ai-danger';del.textContent='Eliminar';del.onclick=function(){api('conversation',{method:'DELETE',query:'&id='+encodeURIComponent(c.id)}).then(function(){row.remove()})};row.appendChild(open);row.appendChild(document.createTextNode(' '));row.appendChild(del);list.appendChild(row)})})};
  api('provider').then(function(d){if(d.provider)document.getElementById('provider').value=d.provider;if(d.model)document.getElementById('model').value=d.model;if(d.has_key){document.getElementById('providerStatus').textContent='Clave configurada ••••'+d.key_suffix;return loadModels()}}).catch(function(){});
  }
  if(document.readyState==='loading')document.addEventListener('DOMContentLoaded',init);else init();
})();
