#!/usr/bin/env python3
"""Operator-only exact runtime/read/write composition in owned disposable state.
Requires a freshly built framework fixture and starter package from the paired
Celln checkout. Never admits the monolithic worker or any other starter tools.
KVM member checking runs in the intended native pod before catalogue creation.
"""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import blake3


def digest(data):
    return 'blake3:' + blake3.blake3(data).hexdigest()


def put(root, data):
    h = digest(data).split(':')[1]
    p = root / 'objects' / h[:2] / h
    p.parent.mkdir(parents=True, exist_ok=True)
    if p.exists():
        assert p.read_bytes() == data, 'corrupt immutable store'
    else:
        p.write_bytes(data)
    return 'blake3:' + h


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--state', type=Path, required=True)
    p.add_argument('--expected-cluster-uid', required=True)
    p.add_argument('--celln', type=Path, required=True)
    p.add_argument('--guests', type=Path, required=True)
    args = p.parse_args()
    spec = importlib.util.spec_from_file_location('runner', Path(__file__).with_name('run.py'))
    mod = importlib.util.module_from_spec(spec); spec.loader.exec_module(mod)
    c = mod.load_cluster(args.state, args.expected_cluster_uid)
    kube = ['kubectl', '--kubeconfig', c['kubeconfig'], '--context', c['context']]
    def run(cmd):
        return subprocess.check_output(list(map(str, cmd)), text=True)
    assert json.loads(run(kube + ['get', 'namespace', 'kube-system', '-o', 'json']))['metadata']['uid'] == args.expected_cluster_uid
    assert not json.loads(run(kube + ['get', 'agentruns', '-A', '-o', 'json']))['items'], 'live roots'
    root = args.state / 'native-root-495'
    output = root / 'artifact-build'
    output.mkdir(mode=0o700)
    package = args.state / 'starter-package'
    report = json.loads((package / 'package.json').read_text())
    bundles = {b['name']: b for b in report['bundles']}
    selected = ['runtime', 'workspace-read', 'workspace-write']
    # Exactly these public source closures and their signed member binaries.
    for name in selected:
        b = bundles[name]
        data = (package / name / 'signed-closure.json').read_bytes()
        assert digest(data) == b['closure']
        put(root / 'closures', data)
        closure = json.loads(data)['closure']
        for alias, member in closure['members'].items():
            filename = {'/worker':'celln-harness-turn', '/pilot-fetch':'pilot-fetch', '/workspace-read':'celln-workspace-read', '/workspace-write':'celln-workspace-write'}[alias]
            binary = (args.guests / filename).read_bytes()
            assert digest(binary) == member['hash']
            put(root / 'tools', binary)
    policy_path = root / 'trusted-closures.json'
    policy = json.loads(policy_path.read_text())
    publisher = bundles['runtime']['publisher']
    assert all(bundles[n]['publisher'] == publisher for n in selected)
    if publisher not in policy['publishers']:
        policy['publishers'].append(publisher)
    staged = policy_path.with_name('artifact-closure-policy.new')
    staged.write_text(json.dumps(policy))
    staged.chmod(0o400)
    os.replace(staged, policy_path)
    def write(name, obj):
        path = output / name
        path.write_text(json.dumps(obj, separators=(',', ':')))
        return path
    plan = write('plan.json', {'apiVersion':'celln.dev/composition-plan-v1', 'sources':[bundles[n]['closure'] for n in selected], 'imageBytes':33554432})
    cli = [args.celln, '--root', root]
    composed = output / 'composition'
    run(cli + ['closure', 'compose', plan, '--key-file', args.state / 'artifact-signing-key', '--output-dir', composed])
    for name, h in [('kernel', report['kernel']), ('runtime/initrd', bundles['runtime']['initrd'])]:
        data = (package / name).read_bytes(); assert digest(data) == h; put(root / 'motes', data)
    template = write('template.json', {'apiVersion':'celln.dev/mote-template-v1','kernel':report['kernel'],'initrd':bundles['runtime']['initrd'],'runtimeExecutable':bundles['runtime']['executable'],'runtimeEntryPoint':'/worker','composerPublisher':publisher})
    th = digest(template.read_bytes())
    candidate = output / 'candidate'
    run(cli + ['closure','prepare-mote','--template',template,'--template-hash',th,'--descriptor',composed/'signed-closure.json','--toolfs',composed/'toolfs.ext2','--mote-store',root/'motes','--output-dir',candidate])
    mote = digest((candidate/'mote.json').read_bytes())
    command = kube + ['-n','celln-review-system-495','exec','deployment/celln-review-native-495','-c','native','--','celln','--root','/state','closure','admit-prepared','--candidate','/state/artifact-build/candidate','--template-hash',th,'--approve-mote',mote,'--mote-store','/state/motes','--tool-store','/state/tools']
    (output/'admission.json').write_text(run(command))
    name_schema={'type':'string','minLength':1,'maxLength':256}
    revision={'type':'integer','minimum':0,'maximum':65536}
    content={'type':'string','minLength':0,'maxLength':4096}
    result={'type':'object','properties':{'revision':revision,'content':content,'error':{'type':'string','minLength':1,'maxLength':1024}},'required':[],'additionalProperties':False}
    def schema(value):
        return {'hash':put(root/'tool-schemas',json.dumps(value,separators=(',',':')).encode())}
    objects=[]
    for operation in ('read','write'):
        name='workspace-'+operation; b=bundles[name]
        fields={'name':name_schema}
        if operation=='write': fields.update(revision=revision,content=content)
        tool={'revision':'scoped-artifacts-v1','description':operation+' bounded parent artifact','supportOwner':'disposable-qualification','publisherKey':publisher,'executable':{'hash':b['executable']},'closure':{'hash':b['closure']},'entryPoint':'/'+name,'invocationABI':'celln.json-stdio/v1','argumentsSchema':schema({'type':'object','properties':fields,'required':list(fields),'additionalProperties':False}),'resultSchema':schema(result),'platform':'linux/amd64','lane':'tool','limits':{'timeoutMillis':30000,'memoryBytes':134217728,'argumentBytes':8192,'outputBytes':32768,'workspace':'none','effects':'external-side-effects' if operation=='write' else 'none','artifacts':{'operation':operation,'maxOperations':4,'maxFiles':8,'maxFileBytes':4096,'maxTotalBytes':16384}}}
        objects.append({'apiVersion':'sympozium.ai/v1alpha1','kind':'ClusterCellnTool','metadata':{'name':name},'spec':tool})
    # Native validates the composed closure's ordered sources against the
    # minimal runtime publisher and explicitly selected read/write tool material.
    profile={'revision':'scoped-artifacts-v2','contractVersion':'celln.json-tools/v1','executable':{'hash':bundles['runtime']['executable']},'closure':{'hash':digest((composed/'signed-closure.json').read_bytes())},'mote':{'hash':mote},'publisherKey':publisher,'entryPoint':'/worker','platform':'linux/amd64','lane':'agent','lifecycles':['enduring'],'limits':{'timeoutMillis':120000,'memoryBytes':134217728,'taskBytes':2048,'outputBytes':65536,'workspace':'none'},'json':{'maxTurns':6,'maxCalls':1}}
    objects.insert(0, {'apiVersion':'sympozium.ai/v1alpha1','kind':'CellnRuntimeProfile','metadata':{'name':'scoped-artifact-runtime-v2'},'spec':profile})
    resources=write('resources.json',{'apiVersion':'v1','kind':'List','items':objects})
    role=json.loads(run(kube+['get','clusterrole','celln-review-controller-495','-o','json']))
    assert role['metadata']['labels']['sympozium.ai/celln-review']=='495'
    rule=next(r for r in role['rules'] if r.get('resources')==['cellnruntimeprofiles','clustercellntools'])
    rule['resourceNames'] += ['scoped-artifact-runtime-v2','workspace-read','workspace-write']
    subprocess.run(kube+['replace','-f','-'],input=json.dumps(role),text=True,check=True,capture_output=True)
    readback=json.loads(run(kube+['get','clusterrole',role['metadata']['name'],'-o','json']))
    assert readback['rules']==role['rules']
    run(kube+['create','-f',resources])
    for obj in objects:
        actual=json.loads(run(kube+['get',obj['kind'],obj['metadata']['name'],'-o','json']))
        assert actual['spec']==obj['spec'], 'catalogue readback mismatch'
    write('manifest.json',{'installedAcceptance':False,'starterPackageHash':digest((package/'package.json').read_bytes()),'sources':selected,'templateHash':th,'mote':mote,'composition':digest((composed/'signed-closure.json').read_bytes()),'resources':objects})
    print('Real KVM member admission and exact catalogue readback passed')

if __name__=='__main__':
    os.umask(0o077)
    main()
