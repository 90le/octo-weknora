"""Offline tests: local adapter server and mocked upstream only."""
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import threading
import unittest
from unittest import mock
import urllib.error
import urllib.request

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('embedding_adapter', HERE / 'embedding-adapter.py')
adapter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(adapter)
TOKEN = 'test-token-do-not-disclose'
ENDPOINT = 'https://example.invalid/embeddings'


def output(indices, dimensions=2560):
    return {'output': {'embeddings': [{'text_index': i, 'embedding': [0.5] * dimensions}
                                      for i in indices]},
            'usage': {'total_tokens': len(indices)}}


def response(payload):
    return io.BytesIO(json.dumps(payload).encode())


class ConfigurationTests(unittest.TestCase):
    def test_invalid_endpoint_fails_before_listening(self):
        for value in (None, '', 'http://example.invalid', 'https://',
                      'https://user:password@example.invalid/',
                      'https://example.invalid/#fragment',
                      'https://example.invalid:bad/', ' https://example.invalid'):
            with self.subTest(value=value):
                env = os.environ.copy()
                env.pop('OCTO_EMBEDDING_UPSTREAM', None)
                if value is not None:
                    env['OCTO_EMBEDDING_UPSTREAM'] = value
                result = subprocess.run([sys.executable, '-B', str(HERE / 'embedding-adapter.py')],
                                        env=env, capture_output=True, text=True, timeout=5)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('OCTO_EMBEDDING_UPSTREAM', result.stderr)
                self.assertNotIn('password', result.stderr)

    def test_https_and_redirect(self):
        with mock.patch.dict(os.environ, {'OCTO_EMBEDDING_UPSTREAM': ENDPOINT}):
            self.assertEqual(adapter.configured_upstream(), ENDPOINT)
        req = urllib.request.Request(ENDPOINT, headers={'Authorization': 'Bearer ' + TOKEN})
        self.assertIsNone(adapter.NoRedirect().redirect_request(req, None, 302, '', {}, ENDPOINT))

    def test_redirect_does_not_forward_credential(self):
        seen = []

        class RedirectHandler(adapter.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_POST(self):
                seen.append((self.path, self.headers.get('Authorization')))
                self.send_response(307)
                self.send_header('Location', '/redirected')
                self.end_headers()

        server = adapter.ThreadingHTTPServer(('127.0.0.1', 0), RedirectHandler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            url = 'http://127.0.0.1:%d/original' % server.server_port
            req = urllib.request.Request(url, data=b'{}', headers={'Authorization': 'Bearer ' + TOKEN})
            with self.assertRaises(urllib.error.HTTPError) as error:
                adapter.UPSTREAM_OPENER.open(req, timeout=5)
            self.assertEqual(error.exception.code, 307)
            self.assertEqual(seen, [('/original', 'Bearer ' + TOKEN)])
        finally:
            server.shutdown()
            thread.join()
            server.server_close()


class AdapterTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = adapter.ThreadingHTTPServer(('127.0.0.1', 0), adapter.Handler)
        cls.server.upstream = ENDPOINT
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()
        cls.url = 'http://127.0.0.1:%d' % cls.server.server_port
        cls.client = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.thread.join()
        cls.server.server_close()

    def post(self, payload, auth='Bearer ' + TOKEN, raw=False):
        body = payload if raw else json.dumps(payload).encode()
        headers = {'Content-Type': 'application/json'}
        if auth is not None:
            headers['Authorization'] = auth
        req = urllib.request.Request(self.url + '/v1/embeddings', data=body, headers=headers)
        try:
            result = self.client.open(req, timeout=5)
        except urllib.error.HTTPError as error:
            result = error
        with result:
            return result.status, json.load(result)

    def test_batches_order_auth_and_usage(self):
        texts = ['text-%d' % i for i in range(12)]
        calls = []
        def fake_open(req, timeout):
            self.assertEqual(req.full_url, ENDPOINT)
            self.assertEqual(req.get_header('Authorization'), 'Bearer ' + TOKEN)
            self.assertEqual(timeout, 55)
            body = json.loads(req.data)
            calls.append(body)
            count = len(body['input']['contents'])
            return response(output(list(reversed(range(count)))))
        with mock.patch.object(adapter.UPSTREAM_OPENER, 'open', side_effect=fake_open):
            status, result = self.post({'model': adapter.MODEL, 'input': texts})
        self.assertEqual(status, 200)
        self.assertEqual([len(c['input']['contents']) for c in calls], [10, 2])
        self.assertEqual([i['text'] for c in calls for i in c['input']['contents']], texts)
        self.assertEqual([v['index'] for v in result['data']], list(range(12)))
        self.assertEqual(result['usage']['total_tokens'], 12)
        self.assertNotIn(TOKEN, json.dumps(result))

    def test_string_and_message(self):
        inputs = ({'model': adapter.MODEL, 'input': 'hello world'},
                  {'model': adapter.MODEL, 'messages': [{'role': 'user', 'content': [
                      {'type': 'text', 'text': 'hello '}, {'type': 'text', 'text': 'world'}]}]})
        for payload in inputs:
            with self.subTest(payload=payload):
                with mock.patch.object(adapter.UPSTREAM_OPENER, 'open', return_value=response(output([0]))) as upstream:
                    status, result = self.post(payload)
                self.assertEqual(status, 200)
                self.assertEqual(json.loads(upstream.call_args.args[0].data)['input']['contents'],
                                 [{'text': 'hello world'}])
                self.assertEqual(len(result['data'][0]['embedding']), adapter.DIMENSIONS)

    def test_batch_messages_preserves_independent_inputs(self):
        texts = ['first', 'second']
        messages = [[{'role': 'user', 'content': [{'type': 'text', 'text': text}]}]
                    for text in texts]
        with mock.patch.object(adapter.UPSTREAM_OPENER, 'open', return_value=response(output([1, 0]))) as upstream:
            status, result = self.post({'model': adapter.MODEL, 'messages': messages})
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(upstream.call_args.args[0].data)['input']['contents'],
                         [{'text': text} for text in texts])
        self.assertEqual([item['index'] for item in result['data']], [0, 1])

    def test_invalid_input_and_auth_do_not_call_upstream(self):
        with mock.patch.object(adapter.UPSTREAM_OPENER, 'open') as upstream:
            for auth in (None, 'Basic x', 'Bearer '):
                self.assertEqual(self.post({'model': adapter.MODEL, 'input': 'x'}, auth=auth)[0], 401)
            cases = ({'model': adapter.MODEL, 'input': []}, {'model': adapter.MODEL, 'input': ['']},
                     {'model': adapter.MODEL, 'input': ['x'] * 65}, {'model': 'other', 'input': 'x'},
                     {'model': adapter.MODEL, 'messages': [None]},
                     {'model': adapter.MODEL, 'messages': [[{'role': 'user', 'content': [
                         {'type': 'text', 'text': 'valid'}]}], [None]]},
                     {'model': adapter.MODEL, 'messages': [{'role': 'user', 'content': [
                         {'type': 'image', 'text': 'x'}]}]})
            for payload in cases:
                self.assertEqual(self.post(payload)[0], 400)
            self.assertEqual(self.post(b'{', raw=True)[0], 400)
            upstream.assert_not_called()

    def test_errors_do_not_leak_token(self):
        errors = (urllib.error.HTTPError(ENDPOINT, 403, TOKEN, {}, None), RuntimeError(TOKEN))
        for error in errors:
            with mock.patch.object(adapter.UPSTREAM_OPENER, 'open', side_effect=error):
                status, result = self.post({'model': adapter.MODEL, 'input': 'x'})
            self.assertEqual(status, 502)
            self.assertNotIn(TOKEN, json.dumps(result))
            if isinstance(error, urllib.error.HTTPError):
                self.assertEqual(result['error']['upstream_status'], 403)

    def test_wrong_dimensions_count_and_indices(self):
        cases = ((['x'], output([0], 2559)), (['x'], output([])),
                 (['x', 'y'], output([0, 0])), (['x', 'y'], output([0, 2])))
        for texts, payload in cases:
            with self.subTest(texts=texts, count=len(payload['output']['embeddings'])):
                with mock.patch.object(adapter.UPSTREAM_OPENER, 'open', return_value=response(payload)):
                    status, result = self.post({'model': adapter.MODEL, 'input': texts})
                self.assertEqual(status, 502)
                self.assertNotIn(TOKEN, json.dumps(result))

    def test_health_and_missing_route(self):
        with self.client.open(self.url + '/health', timeout=5) as result:
            self.assertEqual(json.load(result), {'status': 'ok'})
        with self.assertRaises(urllib.error.HTTPError) as error:
            self.client.open(self.url + '/missing', timeout=5)
        self.assertEqual(error.exception.code, 404)


if __name__ == '__main__':
    unittest.main()
