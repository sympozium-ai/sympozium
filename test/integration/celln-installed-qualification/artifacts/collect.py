#!/usr/bin/env python3
"""Independently read back successful artifact tombstones and budget ledgers.

Only allowlisted evidence is published. Never copy private kubeconfigs,
preparations, Secrets, credentials, signing material or deployment snapshots.
"""
import argparse
import importlib.util
import json
import os
from pathlib import Path
from types import SimpleNamespace


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--state', type=Path, required=True)
    p.add_argument('--expected-cluster-uid', required=True)
    p.add_argument('--evidence', type=Path, required=True)
    p.add_argument('--output', type=Path, required=True)
    a = p.parse_args()
    spec = importlib.util.spec_from_file_location('artifact_runner', Path(__file__).with_name('run.py'))
    assert spec is not None and spec.loader is not None
    m = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(m)
    q = m.Qualification.__new__(m.Qualification)
    q.args = SimpleNamespace(expected_cluster_uid=a.expected_cluster_uid)
    q.cluster = m.load_cluster(a.state, a.expected_cluster_uid)
    q.kube = ['kubectl', '--kubeconfig', q.cluster['kubeconfig'], '--context', q.cluster['context']]
    m.verify_cluster(q, a.expected_cluster_uid)
    report = json.loads((a.evidence / 'report.json').read_text())
    assert report['complete'] and report['fixturesRestored']
    assert report['clusterUID'] == a.expected_cluster_uid
    assert report['installedAcceptance'] is False and report['A03'] is False
    assert report['remainingQualificationResources'] == []
    assert len(report['tenants']) == 2
    assert not json.loads(q.k('get', 'agentruns', '-A', '-o', 'json'))['items']
    assert not q.native('celln', '--root', '/state', 'ps', '--json').strip()
    rows = []
    cells = set()
    parents = set()
    for tenant in report['tenants']:
        turns = tenant['turns']
        assert len(turns) == 3
        m.verify_records([t['native'] for t in turns])
        m.verify_stats(tenant['providerAfterDelete'])
        assert tenant['providerBefore']['attempts'] == 0
        assert tenant['provider'] == tenant['providerAfterDelete']
        assert tenant['providerIdentityBefore'] == tenant['providerIdentityAfter']
        assert tenant['rootAbsent'] and tenant['nativeInventoryEmpty']
        assert len(tenant['crossOwnerDenials']) == 3
        assert {x['operation'] for x in tenant['crossOwnerDenials']} == {'read', 'delete', 'turn'}
        assert all(x['enforcement'] == 'Kubernetes RBAC' and x['reason'] == 'Forbidden' for x in tenant['crossOwnerDenials'])
        native = q.receipt(turns[0]['scoped'])
        assert native == tenant['nativeRootTombstone'] and native['cleanupConfirmed']
        ledgers = q.ledger(tenant['rootUID'])
        q.verify_ledger(ledgers, True)
        assert ledgers == tenant['ledgerAfterDelete']
        for turn in turns:
            n, s = turn['native'], turn['scoped']
            assert n['output'] == s['output']
            assert n['execution']['workspace'] == 'none'
            for key in ('parentId', 'parentIncarnation', 'childId', 'cellId', 'receiptDigest'):
                assert n[key] == s[key]
            cells.add(n['cellId'])
            parents.add(n['parentId'])
        # Explicit allowlist excludes raw public object metadata and annotations.
        rows.append({key: tenant[key] for key in (
            'namespace', 'tenantIdentity', 'rootUID', 'turns',
            'providerBefore', 'providerAfterDelete',
            'providerIdentityBefore', 'providerIdentityAfter',
            'crossOwnerDenials', 'ledgerBeforeDelete', 'ledgerAfterDelete',
            'nativeRootTombstone', 'rootAbsent', 'nativeInventoryEmpty')})
    assert len(cells) == 6 and len(parents) == 2
    evidence = {key: report[key] for key in (
        'installedAcceptance', 'A03', 'fixture', 'epoch', 'clusterUID',
        'providerImage', 'complete', 'artifactContract', 'fixturesRestored',
        'remainingQualificationResources')}
    evidence.update(tenants=rows, independentReadback={
        'nativeRootTombstonesMatch': True, 'closedLedgersMatch': True,
        'publicRootsAbsent': True, 'nativeInventoryEmpty': True,
        'tenantCount': len(rows), 'distinctCellCount': len(cells),
        'distinctParentCount': len(parents),
        'providerAttempts': sum(t['providerAfterDelete']['attempts'] for t in rows),
        'rbacDenials': sum(len(t['crossOwnerDenials']) for t in rows),
    })
    a.output.parent.mkdir(parents=True, exist_ok=True)
    a.output.write_text(json.dumps(evidence, indent=2) + '\n')
    print(json.dumps(evidence['independentReadback'], sort_keys=True))


if __name__ == '__main__':
    os.umask(0o077)
    main()
