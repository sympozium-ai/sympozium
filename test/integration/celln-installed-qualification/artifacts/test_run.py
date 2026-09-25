import copy
import importlib.util
from pathlib import Path
import types
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('artifact_runner', Path(__file__).with_name('run.py'))
assert spec is not None and spec.loader is not None
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


class ArtifactEvidenceChecks(unittest.TestCase):
    def test_assistant_text_is_not_artifact_evidence(self):
        stats = {'attempts': 6, 'events': [
            {'accepted': True, 'turn': i // 2 + 1, 'historyExchanges': i // 2,
             'stage': 'tool-call' if i % 2 == 0 else 'answer', 'toolResultVerified': i % 2 == 1}
            for i in range(6)]}
        runner.verify_stats(stats)
        bad = copy.deepcopy(stats)
        bad['events'][3]['toolResultVerified'] = False
        with self.assertRaises(RuntimeError):
            runner.verify_stats(bad)
        bad = copy.deepcopy(stats)
        bad['attempts'] = 7
        with self.assertRaises(RuntimeError):
            runner.verify_stats(bad)

    def probe(self, response):
        q = object.__new__(runner.Qualification)
        q.name = 'fixture'
        q.cluster = {'context': 'kind-hermes-celln-fixture'}
        q.tenant_configs = {'celln-review-b-495': Path('/owned/b-kubeconfig')}
        q.stats = lambda ns: {'attempts': 6}
        root = {'metadata': {'namespace': 'celln-review-a-495', 'uid': 'root-uid'}}
        q.get = lambda *args: root
        q.save = lambda: None
        q.epoch = 'test'
        result = {}
        with patch.object(runner.subprocess, 'run', return_value=response) as execute:
            q.cross_owner(root, result)
        return result, execute.call_args_list

    def test_cross_owner_probes_are_scoped_and_mutations_dry_run(self):
        result, calls = self.probe(types.SimpleNamespace(returncode=1, stderr='Error from server (Forbidden)'))
        self.assertEqual(len(result['crossOwnerDenials']), 3)
        for call in calls:
            command = call.args[0]
            self.assertIn('/owned/b-kubeconfig', command)
            self.assertIn('celln-review-a-495', command)
            if 'delete' in command or 'create' in command:
                self.assertIn('--dry-run=server', command)
        self.assertTrue(all(row['enforcement'] == 'Kubernetes RBAC' for row in result['crossOwnerDenials']))

    def test_transport_failure_or_unexpected_permission_is_not_denial(self):
        for response in [types.SimpleNamespace(returncode=1, stderr='connection refused'), types.SimpleNamespace(returncode=0, stderr='')]:
            with self.assertRaises(RuntimeError):
                self.probe(response)


if __name__ == '__main__':
    unittest.main()
