#!/usr/bin/env python3
"""Independent read-only enduring collector; exports allowlisted public evidence.
Provider counters are direct snapshots captured by run.py, not ledger derivations.
The live provider is restored after run.py: no attempt is made to reconstruct its
counters from the database. Native tombstones and ledgers are re-read here.
"""
import argparse
import csv
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import run


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--state', type=Path, required=True)
    parser.add_argument('--expected-cluster-uid', required=True, help='independently reviewed dedicated kube-system UID')
    parser.add_argument('--report', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    r = json.loads(args.report.read_text())
    run.check(r['complete'] and r['fixturesRestored'] and not r['installedAcceptance'] and not r['A03'], 'completed partial report required')
    q = object.__new__(run.Qualification)
    q.cluster = run.load_cluster(args.state, args.expected_cluster_uid)
    q.kube = ['kubectl', '--kubeconfig', q.cluster['kubeconfig'], '--context', q.cluster['context']]
    run.verify_cluster(q, args.expected_cluster_uid, r['clusterUID'])
    run.check(len(r['tenants']) == 2 and {t['namespace'] for t in r['tenants']} == {'celln-review-a-495', 'celln-review-b-495'}, 'both exact tenants required')
    profile = q.get('cellnruntimeprofile', 'celln-json-enduring-review-495')['spec']
    evidence = {k: r[k] for k in ('installedAcceptance', 'A03', 'fixture', 'epoch', 'clusterUID', 'providerImage', 'workspaceBlocker')}
    evidence.update(reportSHA256=hashlib.sha256(args.report.read_bytes()).hexdigest(), tenants=[], limitations=['No guest workspace retention: scoped contract supports workspace=none only', 'No browser journey, real LLM, migration or full A01-A12 acceptance', 'Fourth-turn exhaustion was not attempted', 'celln ps has no entries even while enduring parent is ready; it is supplementary child inventory, not independent proof that a retained parent is absent', 'Root teardown is native root tombstone confirmation plus public absence and closed ledger; no controller finalizer was stripped'])
    all_children, all_cells, parents = set(), set(), set()
    for t in r['tenants']:
        ns = t['namespace']
        records = [x['native'] for x in t['turns']]
        run.verify_records(records)
        run.verify_stats(t['provider'])
        run.verify_stats(t['providerAfterDelete'])
        run.check(t['providerBefore']['attempts'] == 0 and t['providerIdentityBefore'] == t['providerIdentityAfter'] and t['providerIdentityAfter']['restartCount'] == 0, 'provider reset/restart could invalidate attempts')
        run.check(t['providerIdentityBefore']['imageID'] == r['providerImage'], 'provider image mismatch')
        public_turns = []
        for i, item in enumerate(t['turns']):
            old = item['native']; scoped = item['scoped']
            native = q.receipt(scoped)
            for key in ('id', 'owner', 'parentId', 'parentIncarnation', 'childId', 'cellId', 'receiptDigest', 'execution', 'substrate'):
                run.check(native[key] == old[key], 'native tombstone mismatch: ' + key)
            run.check(native['cleanupConfirmed'], 'native teardown unconfirmed')
            run.check(native['phase'] == ('Cancelled' if i == 0 else 'Succeeded'), 'wrong native terminal phase')
            if i:
                run.check(native['turnId'] == item['uid'] == scoped['turnId'], 'actual continuation UID mismatch')
            run.check(json.loads(scoped['executionProvenance']) == native['execution'] and json.loads(scoped['substrateProvenance']) == native['substrate'], 'public/native provenance mismatch')
            execution, substrate = native['execution'], native['substrate']
            run.check(execution['lane'] == 'agent' and execution['fetch'] is True and execution['workspace'] == 'none' and execution['tool'] == profile['executable']['hash'], 'worker profile mismatch')
            run.check(substrate['closure']['hash'] == profile['closure']['hash'] and substrate['closure']['members']['/worker']['hash'] == profile['executable']['hash'], 'closure provenance mismatch')
            run.check(substrate['kernel'] and substrate['initrd'] and substrate['toolfs'], 'substrate assets absent')
            all_children.add(native['childId']); all_cells.add(native['cellId']); parents.add(native['parentId'])
            public_turns.append({'ordinal': i + 1, 'publicUID': item['uid'], 'initialRootSnapshot': i == 0, 'liveSnapshotPhase': old['phase'], 'liveSnapshotCleanupConfirmed': old['cleanupConfirmed'], 'nativeTombstone': native, 'nativeChildInventoryAfterTurn': item['nativeInventoryAfterTurn']})
        ledger = q.ledger(t['rootUID']); q.verify_ledger(ledger, True)
        run.check(ledger == t['ledgerAfterDelete'], 'ledger changed after cleanup')
        budget = ledger[0]['budget_id']
        run.check(budget.startswith('sha256:') and all(c in '0123456789abcdef' for c in budget[7:]), 'invalid budget id')
        def rows(sql):
            return list(csv.DictReader(io.StringIO(q.k('-n', run.SYSTEM, 'exec', 'deployment/celln-review-postgres-495', '-c', 'postgres', '--', 'psql', '-U', 'celln_review', '-d', 'celln_review', '--csv', '-c', sql))))
        turns = rows("SELECT turn_id,max_requests,max_output_tokens,reserved_requests,reserved_output_tokens,observed_output_tokens,closed FROM celln_model_turn_budgets WHERE budget_id='" + budget + "' ORDER BY turn_id")
        reservations = rows("SELECT turn_id,request_id,reserved_output_tokens,observed_output_tokens,state,outcome,provider_attempted FROM celln_model_reservations WHERE budget_id='" + budget + "' ORDER BY turn_id,request_id")
        run.check(len(turns) == 3 and len(reservations) == 6, 'unexpected durable turn/reservation count')
        run.check(all(x['reserved_requests'] == '2' and x['reserved_output_tokens'] == '1024' and x['observed_output_tokens'] == '16' and x['closed'] == 't' for x in turns), 'turn ledger totals mismatch')
        run.check(all(x['reserved_output_tokens'] == '512' and x['observed_output_tokens'] == '8' and x['provider_attempted'] == 't' and x['state'] == 'terminal' for x in reservations), 'reservation mismatch')
        run.check(not q.k('-n', ns, 'get', 'agentrun', 'enduring-' + r['epoch'], '--ignore-not-found', '-o', 'json').strip(), 'root remains')
        for kind in ('agentruns', 'agentrunturns', 'agents', 'agentruntimes', 'modelconnections', 'serviceaccounts', 'roles', 'rolebindings'):
            items = json.loads(q.k('-n', ns, 'get', kind, '-l', run.LABEL + '=' + r['epoch'], '-o', 'json'))['items']
            run.check(not items, 'temporary resource remains: ' + kind)
        run.check(run.LABEL not in q.get('namespace', ns)['metadata']['labels'], 'qualification namespace selector remains')
        evidence['tenants'].append({'namespace': ns, 'rootUID': t['rootUID'], 'tenantIdentity': t['tenantIdentity'], 'turns': public_turns, 'providerBefore': t['providerBefore'], 'providerSnapshot': t['provider'], 'providerAfterDelete': t['providerAfterDelete'], 'providerIdentityBefore': t['providerIdentityBefore'], 'providerIdentityAfter': t['providerIdentityAfter'], 'ledger': ledger, 'turnLedgers': turns, 'reservations': reservations, 'rootAbsent': True})
    run.check(len(all_children) == len(all_cells) == 6 and len(parents) == 2, 'cross-tenant distinct identity failure')
    run.check(not q.native('/usr/local/bin/celln', '--root', '/state', 'ps', '--json').strip(), 'native child inventory not empty')
    doctor = [json.loads(line) for line in q.native('/usr/local/bin/celln', '--root', '/state', 'doctor', '--json').splitlines() if line]
    run.check(any(x.get('name') == 'kvm' and x.get('ok') for x in doctor) and any(x.get('can_seal_cells') is True for x in doctor), 'KVM capability not confirmed')
    evidence['doctor'] = doctor
    evidence['distinctChildren'] = len(all_children)
    evidence['distinctCells'] = len(all_cells)
    evidence['distinctParents'] = len(parents)
    evidence['nativeChildInventoryEmpty'] = True
    originals = json.loads((args.report.parent / 'originals.json').read_text())
    for original in originals:
        live = q.get(original['kind'], original['name'], original.get('namespace'))
        run.check(live['spec'] == original['spec'], 'fixture restore not exact: ' + original['kind'])
    run.check(not q.k('get', 'cellnexecutionpolicy', 'enduring-' + r['epoch'], '--ignore-not-found', '-o', 'json').strip(), 'temporary policy remains')
    evidence['fixturesRestored'] = True
    evidence['remainingQualificationResources'] = []
    source = Path(__file__).resolve().parent
    evidence['sourceSHA256'] = {str(p.relative_to(source)): hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(source.rglob('*')) if p.is_file() and p.suffix in ('.py', '.go')}
    evidence['providerBinarySHA256'] = hashlib.sha256((args.state / 'enduring-build/review-provider').read_bytes()).hexdigest()
    evidence['sympoziumSHA'] = subprocess.check_output(['git', '-C', str(source.parents[3]), 'rev-parse', 'HEAD'], text=True).strip()
    encoded = json.dumps(evidence, indent=2) + '\n'
    # Do not emit raw request headers, tokens, Kubernetes Secret objects or full CR snapshots.
    run.check(not any(marker in encoded for marker in ('Bearer ', '-----BEGIN PRIVATE KEY-----', '"token":', '"Authorization":')), 'sensitive marker in export')
    args.output.parent.mkdir(parents=True, exist_ok=True)
    with os.fdopen(os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'w') as f:
        f.write(encoded)
    print('VERIFIED: 6 native tombstones, 2 stable parents, 6 distinct children/cells, 12 independent provider attempts, 2 closed root/6 turn ledgers, 12 terminal reservations; temporary resources absent; acceptance=false')

if __name__ == '__main__':
    main()
