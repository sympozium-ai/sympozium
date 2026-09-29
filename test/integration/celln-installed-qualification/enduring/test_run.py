"""Offline validator unit tests; synthetic values are not installed evidence."""
import copy
import unittest
import run

class Validators(unittest.TestCase):
    def records(self):
        return [dict(parentId='parent', parentIncarnation='incarnation', childId=f'child-{i}', cellId=f'cell-{i}', execution={'lane': 'agent'}, substrate={'kernel': 'test'}, phase='Running' if i == 0 else 'Succeeded', cleanupConfirmed=i != 0) for i in range(3)]

    def stats(self):
        return {'attempts': 6, 'events': [dict(turn=i // 2 + 1, stage='tool-call' if i % 2 == 0 else 'answer', historyExchanges=i // 2, accepted=True) for i in range(6)]}

    def test_valid_lifecycle(self):
        run.verify_records(self.records())

    def test_distinct_child_not_enough(self):
        records = self.records()
        records[1]['cellId'] = records[0]['cellId']
        with self.assertRaises(RuntimeError):
            run.verify_records(records)

    def test_lifecycle_and_provenance_fail_closed(self):
        for key, value in [('parentId', 'wrong'), ('execution', {}), ('cleanupConfirmed', False), ('phase', 'Running')]:
            records = self.records()
            records[2][key] = value
            with self.assertRaises(RuntimeError):
                run.verify_records(records)

    def test_initial_root_is_not_child_cleanup(self):
        records = self.records()
        records[0]['cleanupConfirmed'] = True
        with self.assertRaises(RuntimeError):
            run.verify_records(records)

    def test_independent_attempts(self):
        run.verify_stats(self.stats())
        for change in ('count', 'history', 'rejection', 'sequence'):
            stats = self.stats()
            if change == 'count':
                stats['attempts'] = 7
            elif change == 'history':
                stats['events'][4]['historyExchanges'] = 0
            elif change == 'rejection':
                stats['events'][4]['accepted'] = False
            else:
                stats['events'][4]['stage'] = 'answer'
            with self.assertRaises(RuntimeError):
                run.verify_stats(stats)

    def test_budget_numbers_are_verified(self):
        q = object.__new__(run.Qualification)
        row = dict(max_requests='6', max_output_tokens='3072', max_turns='3', reserved_requests='6', reserved_output_tokens='3072', observed_output_tokens='48', closed='t')
        q.verify_ledger([row], True)
        for key in row:
            changed = copy.deepcopy(row)
            changed[key] = 'wrong'
            with self.assertRaises(RuntimeError):
                q.verify_ledger([changed], True)

if __name__ == '__main__':
    unittest.main()
