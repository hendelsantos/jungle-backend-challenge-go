#!/usr/bin/env python3
"""Destructive broker restart probe for an explicitly disposable Compose.
Exit 2 reports lost accepted messages; exit 0 requires durability.
"""
import json, os, subprocess, time, urllib.request, urllib.error, urllib.parse, uuid, sys
assert os.environ.get('FAULT_DISPOSABLE') == '1'
assert os.environ.get('COMPOSE_PROJECT_NAME')
base=os.environ.get('GATEWAY_URL','http://localhost:9080')
idp=os.environ.get('KEYCLOAK_URL','http://localhost:59080')
compose=['docker','compose','-f','docker-compose.yml']
def dc(*args):
    return subprocess.check_output(compose+list(args),text=True).strip()
def sql(query): return dc('exec','-T','postgres','psql','-U','jungle','-d','jungle','-At','-v','ON_ERROR_STOP=1','-c',query)
def request(path, body=None, token=None):
    headers={}
    if body is not None: headers['Content-Type']='application/json';body=json.dumps(body).encode()
    if token:headers['Authorization']='Bearer '+token
    try:
        with urllib.request.urlopen(urllib.request.Request(base+path,body,headers),timeout=15) as r:return r.status,json.load(r)
    except urllib.error.HTTPError as e:return e.code,json.load(e)
def wait_ready():
    for _ in range(90):
        try:
            if request('/health/ready')[0]==200:return
        except OSError:pass
        time.sleep(1)
    raise RuntimeError('readiness did not recover')
def drain():
    for _ in range(120):
        if sql('SELECT count(*) FROM outbox_events WHERE published_at IS NULL')=='0':return
        time.sleep(1)
    raise RuntimeError('outbox did not drain')
def report(phase, **fields):print(json.dumps(dict(phase=phase,**fields)),flush=True)
wait_ready();drain()
form=urllib.parse.urlencode({'grant_type':'client_credentials','client_id':'wallet-internal-client','client_secret':'local-wallet-internal-client-secret'}).encode()
with urllib.request.urlopen(idp+'/realms/jungle/protocol/openid-connect/token',form) as r: token=json.load(r)['access_token']
dc('pause','localstack')
try:
    assert request('/health/ready')[0]==503
    status,wallet=request('/wallets',{'playerId':str(uuid.uuid4()),'initialBalance':{'amount':'10.00','currency':'BRL'}},token)
    assert status==201,(status,wallet)
    wallet_id=wallet['id']
    pending=sql(f"SELECT count(*) FROM outbox_events WHERE payload_json->'data'->>'walletId'='{wallet_id}' AND published_at IS NULL")
    assert pending=='2',pending
    report('broker-unavailable',readiness=503,wallet=wallet_id,pending=int(pending))
finally:dc('unpause','localstack')
wait_ready();drain()
report('pre-ack-recovery',pending=0,published=sql(f"SELECT count(*) FROM outbox_events WHERE payload_json->'data'->>'walletId'='{wallet_id}' AND published_at IS NOT NULL"))
url=json.loads(dc('exec','-T','localstack','awslocal','sqs','get-queue-url','--queue-name','wager-events.fifo'))['QueueUrl']
def visible():
    return int(json.loads(dc('exec','-T','localstack','awslocal','sqs','get-queue-attributes','--queue-url',url,'--attribute-names','ApproximateNumberOfMessages'))['Attributes']['ApproximateNumberOfMessages'])
before=visible();published=sql('SELECT count(*) FROM outbox_events WHERE published_at IS NOT NULL')
assert before>0
report('before-restart',visible=before,published=int(published))
dc('restart','localstack')
for _ in range(60):
    try:queues=json.loads(dc('exec','-T','localstack','awslocal','sqs','list-queues') or '{}');break
    except (subprocess.CalledProcessError,ValueError):time.sleep(1)
else:raise RuntimeError('broker did not restart')
readiness=request('/health/ready')[0]
report('after-broker-restart',queues=queues,readiness=readiness)
# Runtime no longer recreates lost infrastructure. Only the admin command may.
dc('run','--rm','--no-deps','provision-sqs')
dc('restart','api');wait_ready();drain()
after=visible()
assert sql('SELECT count(*) FROM outbox_events WHERE published_at IS NOT NULL')==published
report('after-reprovision',visible=after,published=int(published),readiness=200,lost=before-after)
dc('pause','postgres')
try:assert request('/health/ready')[0]==503;report('postgres-unavailable',readiness=503)
finally:dc('unpause','postgres')
wait_ready()
if after<before:sys.exit(2)
