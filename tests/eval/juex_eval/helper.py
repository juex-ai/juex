"""Live managed Runtime validation against isolated PostgreSQL and a native device."""
from __future__ import annotations
import argparse,datetime,json,os,pathlib,subprocess,sys,tempfile
import yaml
from . import outcomes,selection,verification

REPO_ROOT=pathlib.Path(__file__).resolve().parents[3]
REPORT_ROOT=REPO_ROOT/'.tmp/reports'

def default_report_dir(kind,run_id):return REPORT_ROOT/kind/run_id

def load_source_config(path):
    try:
        with pathlib.Path(path).open() as source:cfg=yaml.safe_load(source)
    except (OSError,yaml.YAMLError) as error:
        raise ValueError('cannot read explicit provider test configuration') from error
    return selection.validate_config(cfg)

def append_jsonl(path,value):
    with pathlib.Path(path).open('a') as output:output.write(json.dumps(value,ensure_ascii=False)+'\n')

def development_outcome_summary(commands,overall):
    return verification.summarize_outcomes(commands)

def write_development_record(report_dir,run_id,commands_file,provider_summary,compaction_dir,overall,json_path,markdown_path):
    commands=[json.loads(line) for line in commands_file.read_text().splitlines() if line.strip()]
    value=dict(run_id=run_id,status='fail' if overall else 'pass',commands=commands)
    json_path.write_text(json.dumps(value,indent=2))
    markdown_path.write_text('# Managed platform validation\n\n'+value['status']+'\n\n'+ '\n'.join(f"- {c.get('label',c.get('command'))}: {c.get('outcome',c.get('status'))}" for c in commands)+'\n')

def add_live_args(parser):
    parser.add_argument('--config',default=os.environ.get('JUEX_PROVIDER_CONFIG',''))
    parser.add_argument('--selection-seed',default=selection.generated_seed())
    parser.add_argument('--run-id',default=datetime.datetime.now(datetime.UTC).strftime('%Y%m%dT%H%M%SZ'))
    parser.add_argument('--timeout',type=int,default=300)
    parser.add_argument('--report-dir',default='')
    parser.add_argument('--work-root',default='')
    parser.add_argument('--only',default='')
    parser.add_argument('--all-models',action='store_true')

def live_parser():
    parser=argparse.ArgumentParser(description=__doc__)
    add_live_args(parser)
    return parser

def provider_smoke(argv):return run_live(live_parser().parse_args(argv),'provider-smoke')

def run_live(args,kind):
    report=pathlib.Path(args.report_dir or default_report_dir(kind,args.run_id));report.mkdir(parents=True,exist_ok=True,mode=0o700)
    results=[];evidence=None
    try:
        if not args.config:raise ValueError('set JUEX_PROVIDER_CONFIG or --config to an explicit test model file')
        if not os.environ.get('JUEX_TEST_POSTGRES_URL'):raise ValueError('JUEX_TEST_POSTGRES_URL is required; the test role must create isolated databases')
        cfg=load_source_config(args.config)
        chosen,evidence=selection.select(cfg,kind=kind,config_path=pathlib.Path(args.config),seed=args.selection_seed,only=args.only.split(',') if args.only else (),all_models=args.all_models,required_context_window=16384 if kind=='compaction' else 0,command_prefix=[sys.executable,'-m','tests.eval.juex_eval',kind])
        print(json.dumps(evidence.as_dict()),flush=True)
        for candidate in chosen:
            model=next(m for m in cfg['models'] if m['provider']==candidate.provider_id and m['name']==candidate.model_id)
            with tempfile.TemporaryDirectory(prefix='juex-live-',dir=args.work_root or None) as directory:
                path=pathlib.Path(directory)/'model.json'
                with path.open('x') as output:json.dump(model,output)
                path.chmod(0o600)
                environment=dict(os.environ,JUEX_LIVE_MODEL_FILE=str(path))
                test={'compaction':'TestManagedLiveCompaction','integration':'TestManagedLiveConversation','provider-smoke':'TestManagedLiveProviderTools'}[kind]
                command=['go','test','-tags=postgres,integration','./tests/e2e','-run','^'+test+'$','-count=1','-v','-timeout='+str(args.timeout)+'s']
                try:
                    result=subprocess.run(command,cwd=REPO_ROOT,env=environment,text=True,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,timeout=args.timeout+30)
                    status=result.returncode;log=result.stdout
                except subprocess.TimeoutExpired as error:
                    status=1;log=(error.stdout or b'').decode(errors='replace') if isinstance(error.stdout,bytes) else (error.stdout or '')
                    log+='\nmanaged live evaluation timed out\n'
                log=log.replace(model['api_key'],'[REDACTED]')
                log_path=report/(str(len(results)+1)+'.log');log_path.write_text(log);log_path.chmod(0o600)
                passed=status==0 and 'MANAGED_LIVE_EVIDENCE' in log
                results.append(dict(provider_model=candidate.ref,status='pass' if passed else 'fail',log=str(log_path)))
                print(f"{candidate.ref}: {'pass' if passed else 'fail'}; log={log_path}",flush=True)
        passed=bool(results) and all(r['status']=='pass' for r in results)
        outcome=dict(outcome='passed' if passed else 'product_failure',reason='managed live contracts passed' if passed else 'selected model or managed platform failed the live contract',matched_rule='managed-live',blocks_merge=not passed,recommended_action='continue' if passed else 'fix_code',retryable=False)
    except (ValueError,OSError) as error:
        outcome=dict(outcome='environment_failure',reason=str(error),matched_rule='managed-live-environment',blocks_merge=True,recommended_action='fix_environment',retryable=False)
    summary=dict(results=results,**outcome,**(evidence.as_dict() if evidence else {}))
    (report/'summary.json').write_text(json.dumps(summary,indent=2))
    print(outcomes.STRUCTURED_PREFIX+json.dumps(outcome),flush=True)
    return int(outcome['blocks_merge'])
