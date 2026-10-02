"""Offline targeting/redaction regressions; never contacts Kubernetes."""
import copy
import json
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch
import run


UID = '11111111-2222-3333-4444-555555555555'


class Targeting(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.state = Path(self.temp.name)
        (self.state / 'kubeconfig').write_text('synthetic, never used')
        self.cluster = dict(cluster='hermes-celln-unit', context='kind-hermes-celln-unit',
                            node='hermes-celln-unit-control-plane', kubeSystemUID=UID,
                            kubeconfig=str(self.state / 'kubeconfig'), installedAcceptance=False)
        self.save_cluster()

    def save_cluster(self):
        (self.state / 'cluster.json').write_text(json.dumps(self.cluster))

    def test_accepts_new_explicit_dedicated_identity(self):
        self.assertEqual(run.load_cluster(self.state, UID), self.cluster)
        args = SimpleNamespace(state=self.state, expected_cluster_uid=UID,
                               output=self.state / 'output', image='test@sha256:' + 'a' * 64)
        with patch.object(run.Qualification, 'command') as command:
            q = run.Qualification(args)
        command.assert_not_called()
        self.assertEqual(q.report['clusterUID'], UID)
        self.assertFalse(q.report['installedAcceptance'])
        self.assertFalse(q.report['A03'])

    def test_rejects_foreign_inconsistent_or_missing_state(self):
        for field, value in [('cluster', 'production'), ('context', 'ambient'),
                             ('node', 'other-control-plane'), ('kubeSystemUID', 'wrong'),
                             ('kubeconfig', '/not-the-state/kubeconfig'),
                             ('installedAcceptance', True)]:
            with self.subTest(field=field):
                original = self.cluster[field]
                self.cluster[field] = value
                self.save_cluster()
                with self.assertRaises(RuntimeError):
                    run.load_cluster(self.state, UID)
                self.cluster[field] = original
        self.save_cluster()
        for uid in ['', 'wrong', 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee']:
            with self.assertRaises(RuntimeError):
                run.load_cluster(self.state, uid)
        with self.assertRaises(RuntimeError):
            run.load_cluster(Path('relative'), UID)
        (self.state / 'kubeconfig').unlink()
        with self.assertRaises(RuntimeError):
            run.load_cluster(self.state, UID)

    def test_live_and_report_uid_must_match(self):
        q = SimpleNamespace(cluster=self.cluster, get=Mock(return_value={'metadata': {'uid': UID}}))
        run.verify_cluster(q, UID, UID)
        with self.assertRaisesRegex(RuntimeError, 'report cluster UID'):
            run.verify_cluster(q, UID, 'different')
        q.get.return_value = {'metadata': {'uid': 'recreated'}}
        with self.assertRaisesRegex(RuntimeError, 'live cluster UID'):
            run.verify_cluster(q, UID)

    def test_namespace_preflight_before_writes(self):
        for change in ('foreign', 'disabled', 'deleting', 'inactive', 'already-used'):
            with self.subTest(change=change):
                q = object.__new__(run.Qualification)
                q.cluster, q.args = self.cluster, SimpleNamespace(expected_cluster_uid=UID)
                def get(kind, name, ns=None):
                    if name == 'kube-system':
                        return {'metadata': {'uid': UID}}
                    ns = {'metadata': {'labels': {'sympozium.ai/celln-review': '495',
                                                  'sympozium.ai/celln-review-tenant': 'enabled'}},
                          'status': {'phase': 'Active'}}
                    if name == 'celln-review-b-495':
                        if change == 'foreign':
                            ns['metadata']['labels']['sympozium.ai/celln-review'] = 'foreign'
                        elif change == 'disabled':
                            ns['metadata']['labels'].pop('sympozium.ai/celln-review-tenant')
                        elif change == 'deleting':
                            ns['metadata']['deletionTimestamp'] = 'synthetic'
                        elif change == 'inactive':
                            ns['status']['phase'] = 'Terminating'
                        else:
                            ns['metadata']['labels'][run.LABEL] = 'another-run'
                    return ns
                q.get, q.create, q.patch, q.k = get, Mock(), Mock(), Mock()
                with self.assertRaises(RuntimeError):
                    q.setup()
                q.create.assert_not_called()
                q.patch.assert_not_called()
                q.k.assert_not_called()

    def test_command_error_does_not_publish_server_diagnostics(self):
        q = object.__new__(run.Qualification)
        response = subprocess.CompletedProcess(['kubectl'], 1, '', 'sensitive-server-response')
        with patch.object(run.subprocess, 'run', return_value=response):
            with self.assertRaises(RuntimeError) as caught:
                q.command(['kubectl', 'private-argument'])
        self.assertNotIn('sensitive-server-response', str(caught.exception))
        self.assertNotIn('private-argument', str(caught.exception))

    def test_restore_preserves_original_selector_expressions(self):
        q = object.__new__(run.Qualification)
        selector = {'matchLabels': {'review': '495'}, 'matchExpressions': [
            {'key': 'existing', 'operator': 'Exists'}]}
        spec = {'namespaceSelector': selector}
        original = {'kind': 'cellnexecutionpolicy', 'name': 'test-policy', 'spec': spec}
        q.originals, q.created, q.out, q.report = [original], [], self.state, {}
        restored = {'metadata': {'uid': UID, 'resourceVersion': '42'}, 'spec': spec}
        q.patch = Mock(side_effect=[copy.deepcopy(restored), {'metadata': {'labels': {}}},
                                   {'metadata': {'labels': {}}}])
        q.get, q.k, q.save = Mock(return_value=restored), Mock(), Mock()
        q.restore()
        operations = json.loads(q.k.call_args.args[-1])
        self.assertEqual(operations[-1], {'op': 'replace', 'path': '/spec/namespaceSelector', 'value': selector})
        self.assertEqual(operations[0]['value'], UID)
        self.assertEqual(operations[1]['value'], '42')
        self.assertTrue(q.report['fixturesRestored'])


if __name__ == '__main__':
    unittest.main()
