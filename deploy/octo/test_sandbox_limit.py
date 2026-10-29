"""Exercise the dedicated Docker admission plugin without a Docker daemon."""
import concurrent.futures
from pathlib import Path
import tempfile
import unittest


class SandboxLimitTests(unittest.TestCase):
    def test_parallel_admission_and_slot_reuse(self):
        with tempfile.TemporaryDirectory(prefix='octo-limit-test-') as directory:
            source = Path(__file__).with_name('sandbox-limit.py').read_text(encoding='utf-8')
            source = source.replace("STATE='/var/lib/octo-sandbox-limit/reservations.json'",
                                    'STATE=' + repr(str(Path(directory) / 'state.json')))
            source = source.replace("boot=open('/proc/sys/kernel/random/boot_id',encoding='utf8').read().strip()",
                                    "boot='test-boot'")
            source = source.replace('os.fchmod(f.fileno(),0o600);', '')
            source = source.replace('socketserver.UnixStreamServer', 'socketserver.TCPServer')
            namespace = {'__name__': 'octo_limit_test'}
            exec(compile(source, 'sandbox-limit.py', 'exec'), namespace)
            namespace['active_ids'] = lambda: []

            def request(index):
                return namespace['authorize']('/AuthZPlugin.AuthZReq', {
                    'RequestMethod': 'POST',
                    'RequestURI': '/v1.41/containers/container%d/start' % index,
                })

            with concurrent.futures.ThreadPoolExecutor(max_workers=10) as executor:
                results = list(executor.map(request, range(10)))
            winners = [i for i, result in enumerate(results) if result.get('Allow')]
            self.assertEqual(len(winners), 4)
            namespace['active_ids'] = lambda: ['container%d' % i for i in winners]
            for index in winners:
                namespace['authorize']('/AuthZPlugin.AuthZRes', {
                    'RequestMethod': 'POST',
                    'RequestURI': '/v1.41/containers/container%d/start' % index,
                })
            self.assertFalse(request(99).get('Allow'))
            namespace['active_ids'] = lambda: ['container%d' % i for i in winners[:-1]]
            self.assertTrue(request(100).get('Allow'))


if __name__ == '__main__':
    unittest.main()
