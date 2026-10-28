from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request


MODEL = 'qwen3-vl-embedding'
DIMENSIONS = 2560


def configured_upstream():
    """Require an explicit HTTPS endpoint before starting the server."""
    value = os.environ.get('OCTO_EMBEDDING_UPSTREAM', '')
    try:
        if not value or value != value.strip() or any(ord(c) < 33 or ord(c) == 127 for c in value):
            raise ValueError
        parsed = urllib.parse.urlsplit(value)
        if (parsed.scheme.lower() != 'https' or not parsed.hostname or
                parsed.username is not None or parsed.password is not None or
                parsed.fragment or parsed.port == 0):
            raise ValueError
    except ValueError:
        raise ValueError('OCTO_EMBEDDING_UPSTREAM must be a valid HTTPS URL without credentials or a fragment') from None
    return value


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


# Redirects must not forward the client's credential to a different endpoint.
UPSTREAM_OPENER = urllib.request.build_opener(NoRedirect())


def request_texts(data):
    if not isinstance(data, dict):
        return None
    texts = data.get('input')
    if texts is None and isinstance(data.get('messages'), list):
        messages = data['messages']
        if (len(messages) == 1 and isinstance(messages[0], dict) and
                messages[0].get('role') == 'user' and
                isinstance(messages[0].get('content'), list)):
            parts = messages[0]['content']
            if all(isinstance(p, dict) and p.get('type') == 'text' and
                   isinstance(p.get('text'), str) for p in parts):
                texts = [''.join(p['text'] for p in parts)]
    if isinstance(texts, str):
        texts = [texts]
    if (data.get('model') != MODEL or not isinstance(texts, list) or
            not texts or len(texts) > 64 or
            not all(isinstance(t, str) and t for t in texts)):
        return None
    return texts


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def reply(self, status, data):
        body = json.dumps(data).encode()
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == '/health':
            self.reply(200, {'status': 'ok'})
        else:
            self.reply(404, {'error': 'not found'})

    def do_POST(self):
        if self.path != '/v1/embeddings':
            return self.reply(404, {'error': 'not found'})
        auth = self.headers.get('Authorization', '')
        if not auth.startswith('Bearer ') or not auth[7:].strip():
            return self.reply(401, {'error': 'Bearer credential required'})
        try:
            length = int(self.headers.get('Content-Length', '0'))
            if not 0 < length <= 1048576:
                return self.reply(413, {'error': 'request too large'})
            data = json.loads(self.rfile.read(length))
        except (ValueError, UnicodeDecodeError):
            return self.reply(400, {'error': 'invalid JSON request'})
        texts = request_texts(data)
        if texts is None:
            return self.reply(400, {'error': 'only qwen3-vl-embedding text batches supported'})
        try:
            vectors = []
            usage = 0
            for start in range(0, len(texts), 10):
                batch = texts[start:start + 10]
                body = {'model': MODEL, 'input': {'contents': [{'text': t} for t in batch]},
                        'parameters': {'enable_fusion': False}}
                req = urllib.request.Request(
                    self.server.upstream, data=json.dumps(body).encode(),
                    headers={'Authorization': auth, 'Content-Type': 'application/json'})
                with UPSTREAM_OPENER.open(req, timeout=55) as response:
                    out = json.load(response)
                embeddings = out.get('output', {}).get('embeddings', [])
                if len(embeddings) != len(batch):
                    raise ValueError('embedding count mismatch')
                indices = []
                for i, embedding in enumerate(embeddings):
                    index = embedding.get('index', embedding.get('text_index', i))
                    vector = embedding.get('embedding', [])
                    if (type(index) is not int or not 0 <= index < len(batch) or
                            not isinstance(vector, list) or len(vector) != DIMENSIONS):
                        raise ValueError('invalid vector')
                    indices.append(index)
                    vectors.append({'object': 'embedding', 'index': start + index, 'embedding': vector})
                if sorted(indices) != list(range(len(batch))):
                    raise ValueError('duplicate or missing index')
                usage += out.get('usage', {}).get('total_tokens', 0)
            self.reply(200, {'object': 'list', 'model': MODEL,
                             'data': sorted(vectors, key=lambda e: e['index']),
                             'usage': {'prompt_tokens': usage, 'total_tokens': usage}})
        except urllib.error.HTTPError as error:
            self.reply(502, {'error': {'message': 'upstream embedding request failed',
                                       'upstream_status': error.code}})
        except Exception:
            self.reply(502, {'error': {'message': 'embedding conversion failed'}})


def main():
    try:
        upstream = configured_upstream()
    except ValueError as error:
        print(str(error), file=sys.stderr)
        return 1
    server = ThreadingHTTPServer(('0.0.0.0', 8090), Handler)
    server.upstream = upstream
    server.serve_forever()
    return 0


if __name__ == '__main__':
    sys.exit(main())
