"""Verify unattended inspection and query a terminal job on an offline native CPA."""

from datetime import datetime
from email.utils import parsedate_to_datetime
import json
import re
import time


LOGIN = '/v0/management/plugins/model-quality/login'
ACCOUNT = 'qa-readonly-account'
JOB = 'qa-readonly-history-job'
COMMAND = 'qa-readonly-account-command'
EMAIL = 'readonly-account@example.invalid'


def epoch_second(value):
    # Go can emit nine fractional digits. The HTTP Date and our comparison
    # have one-second resolution, including on Python 3.9.
    whole = re.sub(r'\.\d{1,9}(?=Z$|[+-]\d\d:\d\d$)', '', value)
    return int(datetime.fromisoformat(whole.replace('Z', '+00:00')).timestamp())


def check_account_queries(get, previous_inspection=None):
    if not __debug__:
        raise RuntimeError('The native account fixture requires enabled assertions')
    paths = {
        'accounts': '/accounts',
        'jobs': '/jobs?account_id=' + ACCOUNT,
        'logs': '/logs?id=' + JOB,
        'detail': '/detail?id=' + JOB,
        'command': '/commands?id=' + COMMAND,
        'missing': '/commands?id=qa-never-submitted-account-command',
    }
    authenticated = 0
    anonymous = set()
    first_account_request_at = None

    def read(name, status=200):
        nonlocal authenticated, first_account_request_at
        if name not in anonymous:
            # Compare both callers on the same resource as one access-control case.
            code, _, headers = get(LOGIN + paths[name], auth=False)
            if code != 401:
                raise AssertionError(f'anonymous {name} returned {code}, expected 401')
            if name == 'accounts':
                assert authenticated == 0 and not anonymous
                # CPA generated this header before the authenticated call. Both
                # it and the inspection timestamp use the same container clock.
                first_account_request_at = int(parsedate_to_datetime(headers['Date']).timestamp())
            anonymous.add(name)
        code, raw, _ = get(LOGIN + paths[name], auth=True)
        if code != status:
            raise AssertionError(f'authenticated {name} returned {code}, expected {status}')
        authenticated += 1
        return json.loads(raw)

    # The native monitor ticks every 250 ms and persists RFC3339 seconds. Leave
    # a silent interval long enough to compare whole timestamps without polling.
    time.sleep(3)
    accounts = read('accounts')['items']
    assert len(accounts) == 1 and accounts[0]['id'] == ACCOUNT
    account = accounts[0]
    assert account['email'] == EMAIL and account['last_job_id'] == JOB
    assert not account['auth_id'] and not account['auto_relogin']
    assert account['health_status'] == 'not_linked' and account['health_reason'] == 'login_required'
    assert account['revision'] == 2, 'expected exactly one scheduled revision from the seeded revision 1'
    checked = epoch_second(account['last_checked_at'])
    due = epoch_second(account['next_check_at'])
    assert checked < first_account_request_at < due, 'inspection must precede the first authenticated business read'
    assert due - checked == 15 * 60, 'inspection did not advance its configured 15-minute interval'
    inspection = {
        'checked_epoch': checked,
        'next_check_epoch': due,
        'account_revision': account['revision'],
        'health_status': account['health_status'],
        'health_reason': account['health_reason'],
    }
    if previous_inspection is not None:
        assert inspection == previous_inspection, 'restart repeated or changed a completed inspection before it was due'

    jobs, logs = read('jobs'), read('logs')
    assert not jobs['has_more'] and len(jobs['items']) == 1
    assert jobs['items'][0]['id'] == JOB and jobs['items'][0]['account_id'] == ACCOUNT
    assert logs['job_id'] == JOB and logs['retained_count'] == len(logs['entries']) == 1
    assert logs['entries'][0]['status'] == 'cancelled'

    detail = read('detail')
    assert detail['id'] == JOB and detail['account_id'] == ACCOUNT
    assert detail['email'] == EMAIL and detail['can_delete'] is True
    assert detail['status'] == 'cancelled' and detail['revision'] == 3
    # The seed sets created_at to one minute before writing the new fixture.
    created = epoch_second(detail['created_at'])
    assert checked >= created + 60, 'last inspection predates the creation of the isolated database'

    receipt = read('command')
    assert receipt['id'] == COMMAND and receipt['path'] == '/accounts/start'
    assert receipt['status'] == 'accepted' and receipt['job_id'] == JOB
    saved = receipt['job']
    for field in ('id', 'account_id', 'status', 'revision', 'masked_email'):
        assert saved[field] == detail[field], f'durable job field differed: {field}'
    public = json.dumps(receipt, ensure_ascii=False)
    for private in ('"email"', '"authorization_url"', '"credentials"', '"password"', '"totp_secret"',
                    '"api_key"', '"access_token"', '"refresh_token"', EMAIL,
                    'fixture-query-only-password', 'KJCU6TCBFVGU6RCS'):
        assert private not in public, 'command returned private data'

    missing = read('missing', 404)
    assert missing['code'] == 'command_not_accepted' and missing['source'] == 'business'
    assert missing['outcome_unknown'] is False
    assert read('command') == receipt
    assert read('jobs') == jobs, 'receipt lookup changed job history'
    assert read('logs') == logs, 'receipt lookup changed diagnostic history'
    assert anonymous == set(paths)
    return {
        'anonymous_401': len(anonymous),
        'authenticated_reads': authenticated,
        'accounts': len(accounts),
        'first_account_request_epoch': first_account_request_at,
        'inspection': inspection,
        'job_status': detail['status'],
        'command_status': receipt['status'],
        'unaccepted_status': 404,
        'jobs_before_and_after': len(jobs['items']),
        'log_entries_before_and_after': len(logs['entries']),
    }
