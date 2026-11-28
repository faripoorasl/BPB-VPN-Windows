import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:bpb/services/windows/bpbcore_client.dart';

Future<void> main() async {
  final sandbox = await Directory.systemTemp.createTemp('bpb_debug');
  final local = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
  local.listen((request) async {
    print('local server got: ${request.uri}');
    request.response.statusCode = HttpStatus.noContent;
    await request.response.close();
  });

  final infoFile = File('${sandbox.path}/core-info.json');
  final process = await Process.start('core/bpbcore', ['--service'], environment: {
    'BPBVPN_INFO_FILE': infoFile.path,
    'BPBVPN_DATA_DIR': sandbox.path,
  });
  process.stderr.listen((data) => stdout.write('[core] ${utf8.decode(data)}'));

  while (!await infoFile.exists()) {
    await Future<void>.delayed(const Duration(milliseconds: 100));
  }
  final client = BpbCoreClient.instance;
  await client.connect(infoFile: infoFile.path);
  print('connected to service');
  client.states.listen((e) => print('state: ${e.state} err: ${e.error}'));

  final config = jsonEncode({
    'log': {'loglevel': 'warning'},
    'inbounds': [
      {
        'tag': 'mixed-in',
        'listen': '127.0.0.1',
        'port': 10808,
        'protocol': 'mixed',
        'settings': {'auth': 'noauth', 'udp': true},
        'sniffing': {'enabled': false},
      },
    ],
    'outbounds': [
      {'protocol': 'freedom', 'tag': 'proxy'},
      {'protocol': 'freedom', 'tag': 'direct'},
      {'protocol': 'blackhole', 'tag': 'block'},
      {'protocol': 'dns', 'tag': 'dns-out'},
    ],
    'routing': {
      'rules': [
        {
          'type': 'field',
          'inboundTag': ['mixed-in'],
          'port': 53,
          'outboundTag': 'dns-out',
        },
        {
          'type': 'field',
          'ip': ['0.0.0.0/0', '::/0'],
          'outboundTag': 'proxy',
        },
      ],
    },
    'dns': {'servers': ['localhost']},
    'policy': {
      'levels': {'0': {'handshake': 4}},
      'system': {'statsOutboundUplink': true, 'statsOutboundDownlink': true},
    },
    'stats': <String, dynamic>{},
  });

  final start = await client.request('start', {
    'config': config,
    'remark': 'Debug',
    'perAppMode': 'off',
    'apps': <String>[],
    'mtu': 1500,
    'appendHttpProxy': false,
    'proxyOnly': true,
  });
  print('start answer: err=${start.error}');

  await client.states.firstWhere((e) => e.state == 'connected').timeout(const Duration(seconds: 20));
  print('CONNECTED');

  print('calling measureDelay...');
  final watch = Stopwatch()..start();
  final delay = await client.request(
    'measureDelay',
    {'url': 'http://127.0.0.1:${local.port}/204'},
    const Duration(seconds: 30),
  );
  print('delay: ${delay.result} err=${delay.error} in ${watch.elapsedMilliseconds}ms');

  // Proxy fetch through the mixed inbound.
  final http = HttpClient();
  http.findProxy = (uri) => 'PROXY 127.0.0.1:10808';
  final req = await http.getUrl(Uri.parse('http://127.0.0.1:${local.port}/204'));
  final res = await req.close();
  await res.drain();
  print('proxy fetch: ${res.statusCode}');
  http.close();

  await client.request('stop');
  print('stopped');
  await client.request('shutdown', const {}, const Duration(seconds: 10));
  process.kill();
  await local.close();
  await sandbox.delete(recursive: true);
  exit(0);
}
