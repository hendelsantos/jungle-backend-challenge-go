#!/usr/bin/env python3
"""Probe an already running disposable LocalStack; never targets AWS.
Usage: python3 scripts/broker-probe.py http://127.0.0.1:19571 container-name
Container must have PERSISTENCE=1, ENFORCE_IAM=1 and a /var/lib/localstack volume.
Reports observations, not a claim that IAM/durability passed.
"""
import json
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

endpoint, container = sys.argv[1:]
assert urllib.parse.urlparse(endpoint).hostname in ('127.0.0.1', 'localhost')
assert container.startswith('jungle-mission-persistence-')

def call(action, data):
    req = urllib.request.Request(endpoint, json.dumps(data).encode(), {
        'Content-Type': 'application/x-amz-json-1.0', 'X-Amz-Target': 'AmazonSQS.' + action})
    try:
        with urllib.request.urlopen(req, timeout=5) as r:
            return r.status, json.load(r)
    except urllib.error.HTTPError as e:
        return e.code, json.load(e)

def ready():
    for _ in range(60):
        try:
            if call('ListQueues', {})[0] == 200:
                return
        except (OSError, ValueError):
            pass
        time.sleep(1)
    raise RuntimeError('broker did not become ready')

ready()
name = 'audit-' + uuid.uuid4().hex + '.fifo'
status, created = call('CreateQueue', {'QueueName': name, 'Attributes': {'FifoQueue': 'true'}})
assert status == 200, created
url = created['QueueUrl']
policy = {'Version':'2012-10-17','Statement':[{'Effect':'Deny','Principal':'*','Action':'sqs:SendMessage','Resource':'*'}]}
assert call('SetQueueAttributes', {'QueueUrl':url,'Attributes':{'Policy':json.dumps(policy)}})[0] == 200
status, sent = call('SendMessage', {'QueueUrl':url,'MessageBody':'durability-audit','MessageGroupId':'audit','MessageDeduplicationId':uuid.uuid4().hex})
print(json.dumps({'phase':'unsigned-send-with-explicit-deny','http':status,'response':sent}), flush=True)
print(json.dumps({'phase':'before-restart','attributes':call('GetQueueAttributes',{'QueueUrl':url,'AttributeNames':['All']})}), flush=True)
subprocess.run(['docker','restart',container],check=True,stdout=subprocess.DEVNULL)
ready()
print(json.dumps({'phase':'after-restart','queue':call('GetQueueUrl',{'QueueName':name})}),flush=True)
