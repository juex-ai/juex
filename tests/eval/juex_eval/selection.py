"""Select an explicitly configured managed-platform test model without exposing keys."""
from __future__ import annotations
import hashlib,json,pathlib,secrets,shlex
from dataclasses import dataclass

SELECTION_SOURCE='provider_config'
PROVIDER_UNAVAILABLE='provider_unavailable'

@dataclass(frozen=True)
class Candidate:
    provider_id:str
    model_id:str
    ref:str
    context_window:int

@dataclass(frozen=True)
class SelectionEvidence:
    selected_refs:tuple[str,...]
    seed:str
    eligible_refs:tuple[str,...]
    resolved_config_path:str
    redacted_config_hash:str
    reproduction_command:str
    mode:str
    def as_dict(self):
        return dict(selection_source=SELECTION_SOURCE,selected_provider_models=list(self.selected_refs),selected_provider_model=self.selected_refs[0] if len(self.selected_refs)==1 else '',selection_seed=self.seed,eligible_candidate_refs=list(self.eligible_refs),resolved_config_path=self.resolved_config_path,redacted_config_hash=self.redacted_config_hash,reproduction_command=self.reproduction_command,selection_mode=self.mode)

class ProviderUnavailable(ValueError):
    def __init__(self,message,evidence):
        super().__init__(message);self.evidence=evidence

def generated_seed():return secrets.token_hex(8)
def resolved_path(value):return pathlib.Path(value).expanduser().resolve()

def validate_config(cfg):
    if not isinstance(cfg,dict) or set(cfg)!={'models'} or not isinstance(cfg['models'],list):raise ValueError('provider test config requires a models array')
    seen=set()
    allowed={'provider','name','protocol','endpoint','api_key','context_window','max_output'}
    for model in cfg['models']:
        if not isinstance(model,dict) or not allowed<=set(model) or set(model)-allowed-{'output_reserve'}:raise ValueError('each test model requires provider, name, protocol, endpoint, api_key, context_window and max_output; output_reserve is optional for a positive cap')
        if any(not isinstance(model[k],str) or not model[k].strip() for k in ('provider','name','protocol','endpoint','api_key')):raise ValueError('invalid model text field')
        if model['protocol'] not in ('openai/chat','openai/responses','anthropic/messages'):raise ValueError('unsupported test model protocol')
        context,cap,reserve=model['context_window'],model['max_output'],model.get('output_reserve',model['max_output'])
        if any(type(v) is not int for v in (context,cap,reserve)) or context<1024 or cap<0 or reserve<=0 or cap>reserve or reserve>=context:raise ValueError('invalid model output cap or context reservation')
        if model['protocol']=='anthropic/messages' and cap==0 and reserve<4096:raise ValueError('Anthropic default output requires at least 4096 reserved tokens')
        ref=model['provider']+':'+model['name']
        if ref in seen:raise ValueError('duplicate provider:model in test config')
        seen.add(ref)
    return cfg

def enumerate_candidates(cfg):
    validate_config(cfg)
    return sorted([Candidate(m['provider'],m['name'],m['provider']+':'+m['name'],m['context_window']) for m in cfg['models']],key=lambda c:c.ref)

def redacted_config_hash(candidates,cfg=None):
    rows=[{k:v for k,v in m.items() if k!='api_key'} for m in (cfg or {'models':[]})['models']]
    return 'sha256:'+hashlib.sha256(json.dumps(rows,sort_keys=True).encode()).hexdigest()

def select(cfg,*,kind,config_path,seed,only=(),all_models=False,required_context_window=0,command_prefix=()):
    candidates=enumerate_candidates(cfg)
    eligible=[c for c in candidates if c.context_window>=required_context_window]
    requested=list(dict.fromkeys(r.strip() for r in only if r.strip()))
    selected=[];error=''
    if requested:
        by_ref={c.ref:c for c in eligible}
        if any(r not in by_ref for r in requested):error='requested provider:model is unavailable or ineligible'
        else:selected=[by_ref[r] for r in requested]
    elif not eligible:error='no eligible provider:model in explicit test config'
    elif all_models:selected=eligible
    else:selected=[eligible[int(hashlib.sha256(seed.encode()).hexdigest(),16)%len(eligible)]]
    mode='only' if requested else 'all_models' if all_models else 'seeded'
    command=[*command_prefix,'--config',str(resolved_path(config_path)),'--selection-seed',seed]
    if requested:command+=['--only',','.join(requested)]
    elif all_models:command+=['--all-models']
    evidence=SelectionEvidence(tuple(c.ref for c in selected),seed,tuple(c.ref for c in eligible),str(resolved_path(config_path)),redacted_config_hash(candidates,cfg),shlex.join(command),mode)
    if error:raise ProviderUnavailable(error,evidence)
    return selected,evidence

def unavailable_evidence(*,config_path,seed,command_prefix,**_):
    return SelectionEvidence((),seed,(),str(resolved_path(config_path)),'unavailable',shlex.join(command_prefix),'unavailable')
