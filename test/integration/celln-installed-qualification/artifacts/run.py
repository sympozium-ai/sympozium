#!/usr/bin/env python3
"""Owned-cluster, deterministic three-turn KVM context qualification (not A03).
No Secrets are read. TokenRequest credentials live only in owner-only kubeconfigs.
All cluster commands carry the explicit kubeconfig and context. Failures preserve
snapshots/authority for inspection; never remove native/controller finalizers.
"""
import argparse
import copy
import csv
import hashlib
import io
import json
import os
from pathlib import Path
import secrets
import re
import subprocess
import time

API = 'sympozium.ai/v1alpha1'
SYSTEM = 'celln-review-system-495'
LABEL = 'sympozium.ai/enduring-qualification'
OBSERVER = 'qualification.sympozium.ai/enduring-observer'
REVISION = 'scoped-artifacts-v1'



def check(value, message):
    if not value:
        raise RuntimeError(message)


def load_cluster(state, expected_uid):
    """Fail closed on an explicit bootstrap identity before issuing commands.

    The UID must be independently reviewed by the operator, not discovered from
    ambient Kubernetes credentials. A dedicated name is only a targeting guard.
    """
    check(state.is_absolute(), 'absolute dedicated state required')
    check(re.fullmatch(r'[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}', expected_uid), 'explicit cluster UID required')
    cluster = json.loads((state / 'cluster.json').read_text())
    name = cluster.get('cluster', '')
    check(re.fullmatch(r'hermes-celln-[a-z0-9][a-z0-9-]{0,30}', name), 'dedicated bootstrap cluster required')
    check(cluster.get('kubeSystemUID') == expected_uid, 'state cluster UID mismatch')
    check(cluster.get('context') == 'kind-' + name, 'state context mismatch')
    check(cluster.get('node') == name + '-control-plane', 'state node mismatch')
    kubeconfig = Path(cluster.get('kubeconfig', ''))
    check(kubeconfig.is_absolute() and kubeconfig.resolve() == (state / 'kubeconfig').resolve(), 'state-local kubeconfig required')
    check(kubeconfig.is_file(), 'explicit kubeconfig missing')
    check(cluster.get('installedAcceptance') is False, 'partial bootstrap state required')
    return cluster


def verify_cluster(q, expected_uid, report_uid=None):
    check(q.cluster['kubeSystemUID'] == expected_uid, 'state cluster UID mismatch')
    check(q.get('namespace', 'kube-system')['metadata']['uid'] == expected_uid, 'live cluster UID mismatch')
    if report_uid is not None:
        check(report_uid == expected_uid, 'report cluster UID mismatch')


def verify_records(records):
    check(len(records) == 3, 'three native records required')
    for key in ('parentId', 'parentIncarnation'):
        check(all(r.get(key) for r in records) and len({r[key] for r in records}) == 1, 'unstable ' + key)
    for key in ('childId', 'cellId'):
        check(all(r.get(key) for r in records) and len({r[key] for r in records}) == 3, 'non-distinct ' + key)
    for r in records:
        check(r.get('execution') and r.get('substrate'), 'missing real provenance')
    check(records[0]['phase'] == 'Running' and not records[0].get('cleanupConfirmed', False), 'initial root not alive')
    check(all(r['phase'] == 'Succeeded' and r['cleanupConfirmed'] for r in records[1:]), 'turn teardown not confirmed')


def verify_stats(stats):
    check(stats['attempts'] == 6 and len(stats['events']) == 6, 'provider attempts not six')
    for i, e in enumerate(stats['events']):
        turn = i // 2 + 1
        check((i % 2 == 0 or e.get('toolResultVerified') is True) and e['accepted'] and e['turn'] == turn and e['historyExchanges'] == turn - 1 and e['stage'] == ('tool-call' if i % 2 == 0 else 'answer'), 'provider history/sequence mismatch')


class Qualification:
    def __init__(self, args):
        self.args = args
        self.cluster = load_cluster(args.state, args.expected_cluster_uid)
        self.kube = ['kubectl', '--kubeconfig', self.cluster['kubeconfig'], '--context', self.cluster['context']]
        self.epoch = secrets.token_hex(5)
        self.name = 'enduring-' + self.epoch
        self.out = args.output
        self.out.mkdir(mode=0o700)
        self.report = {'installedAcceptance': False, 'A03': False, 'fixture': 'deterministic provider, not real LLM', 'epoch': self.epoch, 'clusterUID': self.cluster['kubeSystemUID'], 'providerImage': args.image, 'tenants': [], 'complete': False, 'artifactContract': 'celln.scoped-artifacts/v1'}
        self.created = []
        self.originals = []
        self.tenant_configs = {}
        self.save()

    def command(self, cmd, obj=None):
        p = subprocess.run(cmd, input=json.dumps(obj) if obj is not None else None, text=True, capture_output=True, timeout=200)
        if p.returncode:
            # API/admission stderr may echo request data. Do not persist it.
            raise RuntimeError('qualification command failed (exit ' + str(p.returncode) + '); inspect retained private resources')
        return p.stdout

    def k(self, *args, obj=None, config=None):
        base = self.kube if config is None else ['kubectl', '--kubeconfig', str(config), '--context', self.cluster['context']]
        return self.command(base + list(args), obj)

    def get(self, kind, name, ns=None):
        return json.loads(self.k(*( ['-n', ns] if ns else []), 'get', kind, name, '-o', 'json'))

    def write(self, name, data):
        p = self.out / name
        p.write_text(json.dumps(data, indent=2) + '\n')
        p.chmod(0o600)

    def save(self):
        self.write('report.json', self.report)
        self.write('created.json', self.created)
        self.write('originals.json', self.originals)

    def create(self, obj, config=None):
        result = json.loads(self.k('create', '-f', '-', '-o', 'json', obj=obj, config=config))
        self.created.append({'apiVersion': result['apiVersion'], 'kind': result['kind'], 'metadata': {k: result['metadata'][k] for k in ('name', 'uid', 'namespace') if k in result['metadata']}})
        self.save()
        return result

    def patch(self, kind, name, patch, ns=None):
        self.k(*( ['-n', ns] if ns else []), 'patch', kind, name, '--type=merge', '-p', json.dumps(patch))
        return self.get(kind, name, ns)

    def obj(self, kind, name, spec=None, ns=None, api=API):
        o = {'apiVersion': api, 'kind': kind, 'metadata': {'name': name, 'labels': {'sympozium.ai/celln-review': '495', LABEL: self.epoch}}}
        if ns:
            o['metadata']['namespace'] = ns
        if spec is not None:
            o['spec'] = spec
        return o

    def native(self, *args):
        return self.k('-n', SYSTEM, 'exec', 'deployment/celln-review-native-495', '-c', 'native', '--', *args)

    def receipt(self, status):
        return json.loads(self.native('cat', '/state/scoped/status/' + status['receiverId'].removeprefix('sha256:')))

    def stats(self, ns):
        return json.loads(self.k('-n', SYSTEM, 'exec', 'deployment/celln-review-gateway-495', '-c', 'gateway', '--', 'curl', '-fsSk', '--retry', '10', '--retry-connrefused', '--retry-delay', '1', '--retry-max-time', '30', '--max-time', '10', f'https://review-provider-495.{ns}.svc:8443/qualification-stats'))

    def provider_identity(self, ns):
        pods = json.loads(self.k('-n', ns, 'get', 'pods', '-l', 'app=review-provider-495', '-o', 'json'))['items']
        pods = [p for p in pods if not p['metadata'].get('deletionTimestamp')]
        check(len(pods) == 1, 'expected one live provider pod')
        pod = pods[0]
        statuses = pod['status']['containerStatuses']
        check(len(statuses) == 1 and statuses[0]['ready'] and statuses[0]['restartCount'] == 0, 'provider restarted/not ready')
        return {'uid': pod['metadata']['uid'], 'name': pod['metadata']['name'], 'containerID': statuses[0]['containerID'], 'imageID': statuses[0]['imageID'], 'restartCount': statuses[0]['restartCount']}

    def ledger(self, uid):
        check(all(c in '0123456789abcdef-' for c in uid), 'invalid UID')
        sql = "SELECT run_uid,budget_id,namespace_uid,max_requests,max_output_tokens,max_turns,reserved_requests,reserved_output_tokens,observed_output_tokens,closed FROM celln_model_budgets WHERE run_uid='" + uid + "'"
        return list(csv.DictReader(io.StringIO(self.k('-n', SYSTEM, 'exec', 'deployment/celln-review-postgres-495', '-c', 'postgres', '--', 'psql', '-U', 'celln_review', '-d', 'celln_review', '--csv', '-c', sql))))

    def wait(self, kind, name, ns, predicate, timeout=150):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            current = self.get(kind, name, ns)
            self.write(ns + '-' + name + '-latest.json', current)
            if predicate(current):
                return current
            st = current.get('status', {})
            if st.get('phase') == 'Failed' or st.get('cellnScoped', {}).get('nativePhase') in ('Failed', 'Refused'):
                raise RuntimeError('terminal execution failure: ' + ns + '/' + name)
            time.sleep(2)
        raise RuntimeError('timeout awaiting ' + ns + '/' + name)

    def tenant(self, ns):
        name = self.name
        self.create(self.obj('ServiceAccount', name, ns=ns, api='v1'))
        role = self.obj('Role', name, ns=ns, api='rbac.authorization.k8s.io/v1')
        role['rules'] = [{'apiGroups': ['sympozium.ai'], 'resources': ['agentruns', 'agentrunturns'], 'verbs': ['get', 'list', 'watch', 'create', 'delete']}]
        self.create(role)
        binding = self.obj('RoleBinding', name, ns=ns, api='rbac.authorization.k8s.io/v1')
        binding.update(roleRef={'apiGroup': 'rbac.authorization.k8s.io', 'kind': 'Role', 'name': name}, subjects=[{'kind': 'ServiceAccount', 'name': name, 'namespace': ns}])
        self.create(binding)
        token = self.k('-n', ns, 'create', 'token', name, '--duration=30m').strip()
        # This only reads the explicitly supplied operator kubeconfig, never an ambient config.
        config = json.loads(self.k('config', 'view', '--raw', '--minify', '-o', 'json'))
        config['users'] = [{'name': 'qualification-tenant', 'user': {'token': token}}]
        for context in config['contexts']:
            context['context']['user'] = 'qualification-tenant'
            context['context']['namespace'] = ns
        path = self.out / (ns + '-private-kubeconfig.json')
        self.write(path.name, config)
        identity = json.loads(self.k('auth', 'whoami', '-o', 'json', config=path))
        check(identity['status']['userInfo']['username'] == 'system:serviceaccount:' + ns + ':' + name, 'tenant identity mismatch')
        return path

    def setup(self):
        verify_cluster(self, self.args.expected_cluster_uid)
        # Validate every namespace before any authority/provider changes.
        for suffix in ('a', 'b', 'system'):
            namespace = self.get('namespace', f'celln-review-{suffix}-495')
            labels = namespace['metadata'].get('labels', {})
            check(namespace.get('status', {}).get('phase') == 'Active' and not namespace['metadata'].get('deletionTimestamp'), 'namespace not active')
            check(labels.get('sympozium.ai/celln-review') == '495' and LABEL not in labels, 'foreign or already used namespace')
            check(labels.get('sympozium.ai/celln-review-tenant', '') == ('' if suffix == 'system' else 'enabled'), 'namespace tenant enablement mismatch')
        check(not json.loads(self.k('get', 'agentruns', '-A', '-o', 'json'))['items'], 'preexisting roots: refuse authority changes')
        check(not self.native('/usr/local/bin/celln', '--root', '/state', 'ps', '--json').strip(), 'native cells preexist')
        original = self.get('cellnexecutionpolicy', 'celln-review-policy-495')
        check(original['metadata']['labels'].get('sympozium.ai/celln-review') == '495', 'foreign policy')
        self.originals.append({'kind': 'cellnexecutionpolicy', 'name': original['metadata']['name'], 'spec': original['spec']})
        new = copy.deepcopy(original['spec'])
        new['namespaceSelector'] = {'matchLabels': {'sympozium.ai/celln-review': '495', 'sympozium.ai/celln-review-tenant': 'enabled', LABEL: self.epoch}}
        new['lifecycles'] = ['enduring']
        new['runtimeProfiles'] = [{'ref': {'name': 'scoped-artifact-runtime-v2', 'revision': 'scoped-artifacts-v2'}}]
        new['tools'] = [{'ref': {'name': 'workspace-' + op, 'revision': REVISION}} for op in ('read', 'write')]
        new['ceilings'].update(maxTurns=3, maxModelRequests=6, maxOutputTokens=3072)
        self.create(self.obj('CellnExecutionPolicy', self.name, new))
        old_selector = copy.deepcopy(original['spec']['namespaceSelector'])
        old_selector.setdefault('matchExpressions', []).append({'key': LABEL, 'operator': 'DoesNotExist'})
        readback = self.patch('cellnexecutionpolicy', original['metadata']['name'], {'spec': {'namespaceSelector': old_selector}})
        check(readback['spec']['namespaceSelector'] == old_selector, 'policy selector write mismatch')
        for t in ('a', 'b'):
            ns = f'celln-review-{t}-495'
            namespace = self.get('namespace', ns)
            check(namespace['metadata']['labels'].get('sympozium.ai/celln-review') == '495' and LABEL not in namespace['metadata']['labels'], 'foreign or already used namespace')
            check(self.patch('namespace', ns, {'metadata': {'labels': {LABEL: self.epoch}}})['metadata']['labels'][LABEL] == self.epoch, 'namespace label write mismatch')
            self.create(self.obj('AgentRuntime', self.name, {'image': '', 'cellnProfileRef': {'name': 'scoped-artifact-runtime-v2', 'revision': 'scoped-artifacts-v2'}}, ns))
            self.create(self.obj('Agent', self.name, {'runtimeRef': self.name, 'authRefs': [{'provider': 'review-fixture', 'secret': 'review-provider-credential'}], 'agents': {'default': {'model': 'review-uppercase'}}}, ns))
            model = copy.deepcopy(self.get('modelconnection', 'review-model', ns)['spec'])
            model['maxOutputTokens'] = 512
            self.create(self.obj('ModelConnection', self.name, model, ns))
            deployment = self.get('deployment', 'review-provider-495', ns)
            self.originals.append({'kind': 'deployment', 'name': 'review-provider-495', 'namespace': ns, 'spec': deployment['spec']})
            self.save()
            containers = copy.deepcopy(deployment['spec']['template']['spec']['containers'])
            check(len(containers) == 1 and containers[0]['name'] == 'fixture', 'unexpected provider layout')
            containers[0]['image'] = self.args.image
            containers[0]['args'] += ['--run-prefix', f'enduring-{t}-{self.epoch}']
            readback = self.patch('deployment', 'review-provider-495', {'spec': {'template': {'spec': {'containers': containers}}}}, ns)
            check(readback['spec']['template']['spec']['containers'] == containers, 'provider patch mismatch')
            self.k('-n', ns, 'rollout', 'status', 'deployment/review-provider-495', '--timeout=120s')
            check(self.stats(ns)['attempts'] == 0, 'provider not fresh')
        self.report['providerIdentities'] = {f'celln-review-{t}-495': self.provider_identity(f'celln-review-{t}-495') for t in ('a', 'b')}
        self.report['fixturesReady'] = True
        self.save()

    def delete(self, obj):
        m = obj['metadata']; api = obj['apiVersion']
        plurals = {'AgentRun': 'agentruns', 'AgentRunTurn': 'agentrunturns', 'ServiceAccount': 'serviceaccounts', 'Role': 'roles', 'RoleBinding': 'rolebindings', 'AgentRuntime': 'agentruntimes', 'Agent': 'agents', 'ModelConnection': 'modelconnections', 'CellnExecutionPolicy': 'cellnexecutionpolicies'}
        base = '/api/v1' if api == 'v1' else '/apis/' + api
        path = base + ('/namespaces/' + m['namespace'] if 'namespace' in m else '') + '/' + plurals[obj['kind']] + '/' + m['name']
        self.k('delete', '--raw', path, '-f', '-', obj={'apiVersion': 'v1', 'kind': 'DeleteOptions', 'preconditions': {'uid': m['uid']}, 'propagationPolicy': 'Background'})

    def cross_owner(self, root, result):
        ns = root['metadata']['namespace']
        foreign = 'celln-review-b-495' if ns == 'celln-review-a-495' else 'celln-review-a-495'
        config = self.tenant_configs[foreign]
        before = self.stats(ns)
        negative_turn = self.obj('AgentRunTurn', self.name + '-foreign', {'runName': self.name, 'runUID': root['metadata']['uid'], 'message': 'foreign-denied'}, ns)
        probes = [
            ('read', ['get', 'agentrun', self.name, '-o', 'json'], None),
            ('delete', ['delete', 'agentrun', self.name, '--dry-run=server'], None),
            ('turn', ['create', '--dry-run=server', '-f', '-'], negative_turn),
        ]
        result['crossOwnerDenials'] = []
        for operation, command, obj in probes:
            response = subprocess.run(['kubectl', '--kubeconfig', str(config), '--context', self.cluster['context'], '-n', ns] + command, input=json.dumps(obj) if obj else None, text=True, capture_output=True, timeout=30)
            check(response.returncode != 0 and '(Forbidden)' in response.stderr, 'foreign operation was not denied by API authorization')
            result['crossOwnerDenials'].append({'operation': operation, 'identity': 'system:serviceaccount:' + foreign + ':' + self.name, 'targetNamespace': ns, 'targetRunUID': root['metadata']['uid'], 'enforcement': 'Kubernetes RBAC', 'reason': 'Forbidden', 'dryRun': operation != 'read'})
        check(self.get('agentrun', self.name, ns)['metadata']['uid'] == root['metadata']['uid'], 'foreign probe changed owner')
        check(self.stats(ns) == before, 'foreign probes caused provider work')
        self.save()

    def run_tenant(self, t):
        ns = f'celln-review-{t}-495'
        for suffix in ('a', 'b'):
            tenant_ns = f'celln-review-{suffix}-495'
            if tenant_ns not in self.tenant_configs:
                self.tenant_configs[tenant_ns] = self.tenant(tenant_ns)
        config = self.tenant_configs[ns]
        result = {'namespace': ns, 'tenantIdentity': 'system:serviceaccount:' + ns + ':' + self.name, 'turns': [], 'providerBefore': self.stats(ns), 'providerIdentityBefore': self.provider_identity(ns)}
        check(result['providerBefore']['attempts'] == 0, 'provider attempts before root')
        self.report['tenants'].append(result)
        messages = [f'enduring-{t}-{self.epoch}-turn-{i}' for i in range(1, 4)]
        spec = {'agentRef': self.name, 'agentId': 'default', 'sessionKey': self.name, 'task': messages[0], 'systemPrompt': 'Use exactly the selected artifact tool requested by the deterministic provider. Never fabricate a tool result.', 'model': {'connectionRef': self.name, 'model': 'review-uppercase'}, 'backend': 'celln', 'executionLifecycle': 'enduring', 'enduring': {'leaseSeconds': 600, 'maxTurns': 3, 'maxModelRequests': 6, 'maxOutputTokens': 3072}, 'timeout': '2m', 'cleanup': 'delete', 'cellnSelection': {'runtimeRef': self.name, 'toolRefs': [], 'clusterToolRefs': [{'name': 'workspace-' + op, 'revision': REVISION} for op in ('read', 'write')]}}
        root_obj = self.obj('AgentRun', self.name, spec, ns)
        root_obj['metadata']['finalizers'] = [OBSERVER]
        root = self.create(root_obj, config)
        result['rootUID'] = root['metadata']['uid']; self.save()
        root = self.wait('agentrun', self.name, ns, lambda o: o.get('status', {}).get('cellnScoped', {}).get('nativePhase') == 'Running' and bool(o['status']['cellnScoped'].get('receiptDigest')))
        for i in range(3):
            if i == 0:
                obj = root
            else:
                turn = self.obj('AgentRunTurn', self.name + '-turn-' + str(i + 1), {'runName': self.name, 'runUID': root['metadata']['uid'], 'message': messages[i]}, ns)
                turn['metadata']['ownerReferences'] = [{'apiVersion': API, 'kind': 'AgentRun', 'name': self.name, 'uid': root['metadata']['uid'], 'controller': True}]
                turn = self.create(turn, config)
                obj = self.wait('agentrunturn', turn['metadata']['name'], ns, lambda o: o.get('status', {}).get('cellnScoped', {}).get('nativePhase') == 'Succeeded' and o['status']['cellnScoped'].get('cleanupConfirmed', False))
                check(obj['status']['cellnScoped']['turnId'] == obj['metadata']['uid'], 'turn not bound to actual UID')
            scoped = obj['status']['cellnScoped']
            receipt = self.receipt(scoped)
            for k in ('parentId', 'parentIncarnation', 'childId', 'cellId', 'receiptDigest'):
                check(scoped.get(k) and receipt.get(k) == scoped[k], 'native/public mismatch: ' + k)
            check(receipt['owner'] == scoped['owner'] and receipt['id'] == scoped['receiverId'], 'native owner mismatch')
            check(scoped.get('output', '').strip() == f'enduring-{t}-{self.epoch}', 'wrong exact output')
            self.write(ns + '-turn-' + str(i + 1) + '.json', obj)
            self.write(ns + '-native-' + str(i + 1) + '.json', receipt)
            result['turns'].append({'uid': obj['metadata']['uid'], 'scoped': scoped, 'native': receipt})
            result['provider'] = self.stats(ns)
            check(self.provider_identity(ns) == result['providerIdentityBefore'], 'provider identity changed during execution')
            inventory = self.native('/usr/local/bin/celln', '--root', '/state', 'ps', '--json')
            result['turns'][-1]['nativeInventoryAfterTurn'] = [json.loads(line) for line in inventory.splitlines() if line.strip()]
            self.save()
        verify_records([r['native'] for r in result['turns']])
        verify_stats(result['provider'])
        self.cross_owner(root, result)
        result['ledgerBeforeDelete'] = self.ledger(root['metadata']['uid'])
        self.verify_ledger(result['ledgerBeforeDelete'], False)
        self.save()
        # Observe teardown while our finalizer holds the public identity/evidence.
        self.delete(root)
        deleted = self.wait('agentrun', self.name, ns, lambda o: o.get('status', {}).get('cellnScoped', {}).get('cleanupConfirmed', False), timeout=120)
        native = self.receipt(deleted['status']['cellnScoped'])
        check(native['cleanupConfirmed'] and native['parentId'] == result['turns'][0]['native']['parentId'], 'root native cleanup mismatch')
        result['rootAfterDelete'] = deleted
        result['nativeRootTombstone'] = native
        result['ledgerAfterDelete'] = self.ledger(root['metadata']['uid'])
        self.verify_ledger(result['ledgerAfterDelete'], True)
        self.save()
        # Remove ONLY our observer; controller/native finalizers are untouched.
        finalizers = deleted['metadata'].get('finalizers', [])
        self.k('-n', ns, 'patch', 'agentrun', self.name, '--type=json', '-p', json.dumps([{'op': 'test', 'path': '/metadata/uid', 'value': root['metadata']['uid']}, {'op': 'test', 'path': '/metadata/resourceVersion', 'value': deleted['metadata']['resourceVersion']}, {'op': 'replace', 'path': '/metadata/finalizers', 'value': [v for v in finalizers if v != OBSERVER]}]))
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            if not self.k('-n', ns, 'get', 'agentrun', self.name, '--ignore-not-found', '-o', 'json').strip():
                break
            time.sleep(1)
        else:
            raise RuntimeError('root remains after native cleanup')
        check(not self.native('/usr/local/bin/celln', '--root', '/state', 'ps', '--json').strip(), 'live native inventory after root deletion')
        result['rootAbsent'] = True; result['nativeInventoryEmpty'] = True
        result['providerAfterDelete'] = self.stats(ns); verify_stats(result['providerAfterDelete'])
        result['providerIdentityAfter'] = self.provider_identity(ns)
        check(result['providerIdentityAfter'] == result['providerIdentityBefore'], 'provider restarted or replaced before snapshot')
        self.save()

    def verify_ledger(self, rows, closed):
        check(len(rows) == 1, 'expected one root budget')
        expected = {'max_requests': '6', 'max_output_tokens': '3072', 'max_turns': '3', 'reserved_requests': '6', 'reserved_output_tokens': '3072', 'observed_output_tokens': '48', 'closed': 't' if closed else 'f'}
        check(all(rows[0].get(k) == v for k, v in expected.items()), 'unexpected actual ledger: ' + json.dumps(rows))

    def restore(self):
        for original in reversed(self.originals):
            restored = self.patch(original['kind'], original['name'], {'spec': original['spec']}, original.get('namespace'))
            if original['kind'] == 'deployment':
                check(restored['spec']['template']['spec']['containers'] == original['spec']['template']['spec']['containers'], 'provider restore mismatch')
                self.k('-n', original['namespace'], 'rollout', 'status', 'deployment/' + original['name'], '--timeout=120s')
            else:
                # Replace the entire selector, preserving original expressions.
                # Merge-patch cannot remove only the qualification expression.
                self.k('patch', original['kind'], original['name'], '--type=json', '-p', json.dumps([
                    {'op': 'test', 'path': '/metadata/uid', 'value': restored['metadata']['uid']},
                    {'op': 'test', 'path': '/metadata/resourceVersion', 'value': restored['metadata']['resourceVersion']},
                    {'op': 'replace', 'path': '/spec/namespaceSelector', 'value': original['spec']['namespaceSelector']},
                ]))
                check(self.get(original['kind'], original['name'])['spec'] == original['spec'], 'policy restore mismatch')
        for t in ('a', 'b'):
            ns = f'celln-review-{t}-495'
            check(LABEL not in self.patch('namespace', ns, {'metadata': {'labels': {LABEL: None}}})['metadata']['labels'], 'namespace restore mismatch')
        for obj in reversed(self.created):
            m = obj['metadata']
            if self.k(*( ['-n', m['namespace']] if 'namespace' in m else []), 'get', obj['kind'], m['name'], '--ignore-not-found', '-o', 'json').strip():
                self.delete(obj)
        remaining = []
        for obj in self.created:
            m = obj['metadata']
            if self.k(*( ['-n', m['namespace']] if 'namespace' in m else []), 'get', obj['kind'], m['name'], '--ignore-not-found', '-o', 'json').strip():
                remaining.append(obj)
        check(not remaining, 'temporary resources remain')
        for private in self.out.glob('*-private-kubeconfig.json'):
            private.unlink()
        self.report['fixturesRestored'] = True
        self.report['remainingQualificationResources'] = []
        self.save()


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--state', type=Path, required=True)
    p.add_argument('--expected-cluster-uid', required=True, help='independently reviewed kube-system UID from the dedicated bootstrap')
    p.add_argument('--output', type=Path, required=True)
    p.add_argument('--image', required=True)
    args = p.parse_args()
    check(re.fullmatch(r'[A-Za-z0-9._/:+-]+@sha256:[0-9a-f]{64}', args.image), 'digest-pinned provider image required')
    os.umask(0o077)
    q = Qualification(args)
    try:
        q.setup()
        for t in ('a', 'b'):
            q.run_tenant(t)
        for key in ('parentId', 'parentIncarnation'):
            check(len({r['turns'][0]['native'][key] for r in q.report['tenants']}) == 2, 'foreign parent reused')
        for key in ('childId', 'cellId'):
            check(len({turn['native'][key] for r in q.report['tenants'] for turn in r['turns']}) == 6, 'foreign child reused')
        q.restore()
        q.report['complete'] = True
        q.save()
        print('PASS: A/B each three turns, actual write/read/read results, six independent attempts, closed ledgers and root teardown. installedAcceptance=false A03=false')
    except Exception as e:
        q.report['error'] = str(e)
        q.save()
        print('BLOCKED: ' + str(e))
        print('Evidence/retained authority: ' + str(q.out))
        raise

if __name__ == '__main__':
    main()
