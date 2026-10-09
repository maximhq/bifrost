"""Fast regressions for the container runner's polling and payload assertions."""
import copy
import json
import pathlib
import tempfile
import unittest
from unittest.mock import Mock

from run import assert_recovered_payload, messages, pending_files


class FixtureTests(unittest.TestCase):
    """Check fixture polling and assertions without starting containers."""
    def test_poll_tolerates_completed_file_disappearing(self):
        """Verify that a worker removing a record does not break fixture polling."""
        gone = Mock()
        gone.read_text.side_effect = FileNotFoundError()
        retained = Mock()
        retained.read_text.return_value = json.dumps({'log_id': 'still-pending'})
        directory = Mock()
        directory.glob.return_value = [gone, retained]
        self.assertEqual(pending_files(directory), {'still-pending': retained})

    def test_invalid_record_is_not_silently_skipped(self):
        """Verify that malformed outbox JSON fails the fixture instead of hiding corruption."""
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            (root / 'upload-invalid.json').write_text('broken JSON')
            with self.assertRaises(json.JSONDecodeError):
                pending_files(root)

    def test_pending_uploads_and_deletions_are_counted_separately(self):
        """Verify that cleanup records cannot count as uploads in quota assertions."""
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            upload, cleanup = root / 'upload-pending.json', root / 'delete-pending.json'
            upload.write_text(json.dumps({'log_id': 'upload'}))
            cleanup.write_text(json.dumps({'log_id': 'cleanup', 'payload': None}))
            self.assertEqual(pending_files(root), {'upload': upload})
            self.assertEqual(pending_files(root, 'delete'), {'cleanup': cleanup})

    def test_recovery_checks_every_message_role_and_content(self):
        """Verify that corruption of any message role or content fails recovery assertions."""
        label = 'fixture'
        value = {'input_history': messages(label),
                 'output_message': {'content': 'ASSISTANT-RESPONSE-LATEST-USER-' + label}}
        assert_recovered_payload(value, label)
        for index in range(4):
            for field in ['role', 'content']:
                with self.subTest(index=index, field=field):
                    corrupted = copy.deepcopy(value)
                    corrupted['input_history'][index][field] = 'corrupted'
                    with self.assertRaises(AssertionError):
                        assert_recovered_payload(corrupted, label)


if __name__ == '__main__':
    unittest.main()
