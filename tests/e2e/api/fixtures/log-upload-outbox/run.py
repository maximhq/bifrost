"""Exercise the actual one-minute outbox task with local S3/provider containers."""
import argparse
import json
import os
import pathlib
import shutil
import sqlite3
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

FIXTURE = pathlib.Path(__file__).resolve().parent
REPO = FIXTURE.parents[4]
BASE = 'http://127.0.0.1:18693'
S3 = 'http://127.0.0.1:18694'


def request(url, body=None, method=None):
    """Send a fixture HTTP request, require status 200, and decode its JSON response."""
    req = urllib.request.Request(url, data=None if body is None else json.dumps(body).encode(),
                                 method=method,
                                 headers={'Content-Type': 'application/json', 'X-Bifrost-Setup-Token': 'local-s3-log-test'})
    with urllib.request.urlopen(req, timeout=35) as response:
        assert response.status == 200
        return json.load(response)


def wait_for(predicate, seconds=90):
    """Poll until the predicate returns a truthy value or raise on timeout."""
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(.2)
    raise AssertionError('Timed out waiting for outbox state')


def pending_files(directory, prefix='upload'):
    """Index upload or deletion records by log ID, tolerating completed removals."""
    pending = {}
    for path in directory.glob(prefix + '-*.json'):
        try:
            record = json.loads(path.read_text())
        except FileNotFoundError:
            continue  # The retry worker completed between listing and reading.
        pending[record['log_id']] = path
    return pending


def messages(label):
    """Build a labeled four-message history for exact recovery assertions."""
    return [{'role': 'system', 'content': 'SYSTEM-CONTEXT-' + label},
            {'role': 'user', 'content': 'EARLIER-USER-' + label},
            {'role': 'assistant', 'content': 'EARLIER-ASSISTANT-' + label},
            {'role': 'user', 'content': 'LATEST-USER-' + label}]


def assert_recovered_payload(value, label):
    """Require every original message and the full deterministic response."""
    assert value['input_history'] == messages(label)
    assert value['output_message']['content'] == 'ASSISTANT-RESPONSE-LATEST-USER-' + label


def run(args):
    """Exercise recovery, deletion, restart, and eviction in a fresh local container stack."""
    work = pathlib.Path(args.work_dir).resolve() if args.work_dir else pathlib.Path(tempfile.mkdtemp(prefix='bifrost-outbox-'))
    binary = pathlib.Path(args.binary).resolve()
    assert binary.is_file(), 'Build a gateway binary first: ' + str(binary)
    assert not (work / 'data/logs.db').exists(), 'Use a fresh test directory'
    for folder in ['data', 'outbox', 'objects', 'results']:
        (work / folder).mkdir(parents=True, exist_ok=True)
    # Runtime image uses UID 1000, GID 0. Its mounted DB/outbox paths must be writable.
    for folder in ['data', 'outbox']:
        (work / folder).chmod(0o770)
    shutil.copyfile(FIXTURE / 'config.json', work / 'data/config.json')
    env = dict(os.environ, OUTBOX_TEST_DIR=str(work), OUTBOX_TEST_BINARY=str(binary), OUTBOX_TEST_MAX_BYTES='10000000000')
    compose_args = ['docker', 'compose', '-f', str(FIXTURE / 'compose.yaml')]

    def compose(*arguments):
        """Run a Docker Compose operation with this fixture run's mounts and settings."""
        subprocess.run(compose_args + list(arguments), env=env, check=True)

    def rows():
        """Read the gateway log rows directly from the fixture SQLite database."""
        with sqlite3.connect(work / 'data/logs.db') as db:
            db.row_factory = sqlite3.Row
            return {row['id']: dict(row) for row in db.execute('SELECT * FROM logs')}

    def files():
        """List both pending upload files and protected deletion records."""
        return list((work / 'outbox').glob('*.json'))

    def pending():
        """Index upload records without including deletion cleanup metadata."""
        return pending_files(work / 'outbox')

    def deletions():
        """Index cleanup records retained for failed remote deletions."""
        return pending_files(work / 'outbox', 'delete')

    def healthy():
        """Report gateway readiness, treating connection failures as not ready."""
        try:
            return request(BASE + '/health')
        except (urllib.error.URLError, ConnectionError):
            return False

    def record(log_id):
        """Fetch a log through the gateway API to check payload hydration."""
        value = request(BASE + '/api/logs/' + log_id)
        return value.get('log', value)

    def create(mode, label):
        """Submit a labeled completion and verify its uploaded or pending log state."""
        request(S3 + '/control', {'mode': mode})
        previous = set(rows())
        response = request(BASE + '/v1/chat/completions', {'model': 'openai/gpt-4o-mini', 'messages': messages(label)})
        assert response['choices'][0]['message']['content'] == 'ASSISTANT-RESPONSE-LATEST-USER-' + label
        log_id = wait_for(lambda: next((key for key, row in rows().items() if key not in previous and row['status'] == 'success'), None))
        if mode == 'ok':
            wait_for(lambda: rows()[log_id]['has_object'])
        else:
            wait_for(lambda: pending().get(log_id))
            assert not rows()[log_id]['has_object']
            partial = record(log_id)
            assert partial['status'] == 'success' and len(partial['input_history']) == 1
            assert not partial.get('output_message')
        print(json.dumps({'created': label, 'log_id': log_id, 's3': mode}), flush=True)
        return log_id

    def assert_recovered(log_id, label):
        """Wait for recovery, check the complete payload, and save the API response."""
        wait_for(lambda: rows()[log_id]['has_object'])
        wait_for(lambda: log_id not in pending())
        value = record(log_id)
        assert_recovered_payload(value, label)
        (work / ('results/' + label + '-recovered.json')).write_text(json.dumps(value, indent=2))
        print(json.dumps({'recovered': label, 'log_id': log_id}), flush=True)

    try:
        compose('up', '-d')
        wait_for(healthy)
        create('ok', 'healthy')
        periodic = create('denied', 'outbox-periodic')
        path = pending()[periodic]
        original = path.read_bytes()
        initial_attempts = len(request(S3 + '/control')['put_attempts'])
        # Wait for the real ticker to retry while writes are still denied.
        wait_for(lambda: len(request(S3 + '/control')['put_attempts']) > initial_attempts)
        assert path.read_bytes() == original and not rows()[periodic]['has_object']
        request(S3 + '/control', {'mode': 'ok'})
        assert_recovered(periodic, 'outbox-periodic')

        deleted_pending = create('denied', 'outbox-delete-pending')
        request(BASE + '/api/logs', {'ids': [deleted_pending]}, method='DELETE')
        assert deleted_pending not in rows() and deleted_pending not in pending()
        cleanup = json.loads(deletions()[deleted_pending].read_text())
        assert cleanup['payload'] is None, 'Explicit deletion must erase pending content immediately'
        request(S3 + '/control', {'mode': 'ok'})
        wait_for(lambda: deleted_pending not in deletions())
        s3_state = request(S3 + '/control')
        assert not any(deleted_pending in key for key in s3_state['objects'])
        assert any(deleted_pending in attempt['path'] and attempt['status'] == 204 for attempt in s3_state['delete_attempts'])

        restarted = create('unavailable', 'outbox-restart')
        disconnected = create('disconnect', 'outbox-disconnect')
        compose('kill', '-s', 'SIGKILL', 'bifrost')
        compose('start', 'bifrost')
        wait_for(healthy)
        assert files(), 'Restart must preserve pending records'
        request(S3 + '/control', {'mode': 'ok'})
        assert_recovered(restarted, 'outbox-restart')
        assert_recovered(disconnected, 'outbox-disconnect')

        # A real uploaded object whose deletion fails must remain discoverable
        # after its DB row is gone, even across payload eviction and restart.
        deleted = create('ok', 'outbox-delete')
        request(S3 + '/control', {'mode': 'denied'})
        request(BASE + '/api/logs', {'ids': [deleted]}, method='DELETE')
        assert deleted not in rows()
        cleanup_path = deletions()[deleted]
        assert json.loads(cleanup_path.read_text())['payload'] is None
        assert any(deleted in key for key in request(S3 + '/control')['objects'])
        cleanup_age = time.time() - 7200
        os.utime(cleanup_path, (cleanup_age, cleanup_age))

        # Three upload failures, with a budget that fits only the two newest.
        evicted = [create('denied', 'outbox-cap-' + str(i)) for i in range(3)]
        capped_files = pending()
        assert set(capped_files) == set(evicted)
        for i, log_id in enumerate(evicted):
            age = time.time() - 3600 + i * 60
            os.utime(capped_files[log_id], (age, age))
        cap = sum(capped_files[log_id].stat().st_size for log_id in evicted[1:])
        env['OUTBOX_TEST_MAX_BYTES'] = str(cap)
        compose('up', '-d', '--force-recreate', 'bifrost')
        wait_for(healthy)
        wait_for(lambda: len(pending()) == 2)
        assert not capped_files[evicted[0]].exists(), 'Oldest payload must be evicted first'
        assert cleanup_path.exists(), 'Quota eviction must retain failed deletion identifiers'
        assert sum(path.stat().st_size for path in pending().values()) <= cap
        assert all(not rows()[log_id]['has_object'] for log_id in evicted)
        compose('kill', '-s', 'SIGKILL', 'bifrost')
        compose('start', 'bifrost')
        wait_for(healthy)
        assert cleanup_path.exists(), 'Deletion cleanup must survive restart after eviction'
        request(S3 + '/control', {'mode': 'ok'})
        for i, log_id in enumerate(evicted[1:], 1):
            assert_recovered(log_id, 'outbox-cap-' + str(i))
        wait_for(lambda: deleted not in deletions())
        s3_state = request(S3 + '/control')
        assert not any(deleted in key for key in s3_state['objects'])
        assert any(deleted in attempt['path'] and attempt['status'] == 204 for attempt in s3_state['delete_attempts'])
        assert not rows()[evicted[0]]['has_object']
        assert not files(), 'Successful recovery must retire uploads and deletion records'
        report = {'status': 'PASS', 'periodic_retry': periodic, 'restart_retry': restarted,
                  'deleted_pending_log': deleted_pending, 'deleted_uploaded_log': deleted,
                  'cleanup_survived_eviction_and_restart': True,
                  'connection_failure_retry': disconnected, 'restart_method': 'SIGKILL then start',
                  'oldest_evicted': evicted[0], 'newest_recovered': evicted[1:], 'max_bytes': cap,
                  'remaining_outbox_files': len(files())}
        (work / 'results/result.json').write_text(json.dumps(report, indent=2))
        (work / 'results/newman.env.json').write_text(json.dumps({'name': 'Local outbox fixture', 'values': [
            {'key': 'baseUrl', 'value': BASE, 'enabled': True},
            {'key': 'include_preview', 'value': '1', 'enabled': True},
            {'key': 'outboxRecoveredLogID', 'value': periodic, 'enabled': True},
            {'key': 'outboxDeletedLogID', 'value': deleted, 'enabled': True},
            {'key': 'outboxS3Url', 'value': S3, 'enabled': True},
            {'key': 'outboxRestartLogID', 'value': restarted, 'enabled': True}]}))
        print(json.dumps(report, indent=2), flush=True)
    finally:
        log = subprocess.run(compose_args + ['logs', '--no-color', 'bifrost'], env=env, capture_output=True, text=True)
        (work / 'results/container.log').write_text(log.stdout + log.stderr)
        print('Artifacts: ' + str(work / 'results'), flush=True)
        if not args.keep:
            compose('down')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', default=str(REPO / 'tmp/outbox-container/main'))
    parser.add_argument('--work-dir')
    parser.add_argument('--keep', action='store_true', help='Keep containers running for the provider-harness fixture cases')
    run(parser.parse_args())
