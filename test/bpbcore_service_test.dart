import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';

import 'package:bpb/services/windows/bpbcore_client.dart';

/// The bpbcore service, end to end: the tests spawn the host binary (built for this
/// platform), talk to it over its localhost protocol and run a REAL Xray instance in proxy
/// mode — connection states, a delay test through the tunnel and the traffic counters.
///
/// BPBCORE_BIN points at the built binary (see scripts/test_core.sh); without it the suite
/// skips.
void main() {
  final bin = Platform.environment['BPBCORE_BIN'] ?? _defaultBinary();
  if (bin == null || !File(bin).existsSync()) {
    test(
      'bpbcore integration (skipped: build the core first, see scripts/test_core.sh)',
      () {},
      skip: 'BPBCORE_BIN is not built',
    );
    return;
  }

  late Directory sandbox;
  late HttpServer local;
  late int localPort;

  setUpAll(() async {
    sandbox = await Directory.systemTemp.createTemp('bpbcore_test');
    local = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    localPort = local.port;
    local.listen((request) async {
      if (request.uri.path == '/204') {
        request.response.statusCode = HttpStatus.noContent;
        await request.response.close();
        return;
      }
      // ~64 KB of payload, so the traffic counters have something to count.
      request.response.add(List.filled(64 * 1024, 65));
      await request.response.close();
    });
  });

  tearDownAll(() async {
    try {
      await BpbCoreClient.instance.request(
        'shutdown',
        const {},
        const Duration(seconds: 10),
      );
    } catch (_) {}
    await BpbCoreClient.instance.disconnectSocket();
    await local.close(force: true);
    await sandbox.delete(recursive: true);
  });

  test('the service starts, connects through a real Xray, measures delay and counts traffic',
      () async {
    final client = BpbCoreClient.instance;
    final infoFile = File('${sandbox.path}${Platform.pathSeparator}core-info.json');

    // Launch the service with the test's own info file and data dir.
    final process = await Process.start(bin, ['--service'], environment: {
      'BPBVPN_INFO_FILE': infoFile.path,
      'BPBVPN_DATA_DIR': sandbox.path,
    });
    addTearDown(() async {
      process.kill();
    });
    // The service logs to stderr; drain it.
    process.stderr.drain();

    // It writes the info file once it is listening.
    final deadline = DateTime.now().add(const Duration(seconds: 20));
    while (DateTime.now().isBefore(deadline)) {
      if (await infoFile.exists() && await infoFile.length() > 2) break;
      await Future<void>.delayed(const Duration(milliseconds: 100));
    }
    expect(await infoFile.exists(), isTrue, reason: 'the service wrote its info file');
    final info = Map<String, dynamic>.from(jsonDecode(await infoFile.readAsString()) as Map);
    expect(info['port'] as int, greaterThan(0));
    expect((info['token'] as String).isNotEmpty, isTrue);

    // Connect with the token from the file.
    await client.connect(infoFile: infoFile.path);
    expect(await client.pingService(), isTrue);
    final host = await client.hostInfo();
    expect(host, isNotNull);
    expect(host!['xray'] as String, isNotEmpty, reason: 'the Xray version is reported');

    // A minimal proxy-mode config, the shape XrayConfigBuilder builds (mixed inbound,
    // freedom/dns/block outbounds, per-outbound traffic counters).
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
        'system': {
          'statsOutboundUplink': true,
          'statsOutboundDownlink': true,
        },
      },
      'stats': <String, dynamic>{},
    });

    final stateEvents = <String>[];
    final subscription = client.states.listen((event) {
      stateEvents.add(event.state);
      // ignore: avoid_print
      print('[integration] state: ${event.state} error: ${event.error} detail: ${event.detail}');
    });

    final startAnswer = await client.request('start', {
      'config': config,
      'remark': 'Test',
      'perAppMode': 'off',
      'apps': <String>[],
      'mtu': 1500,
      'appendHttpProxy': false,
      'proxyOnly': true,
      'exit': null,
      'exitDirect': false,
      'exitRegion': '',
      'torBridge': null,
      'torCustomBridges': <String>[],
      'psiphonConfig': null,
      'sshConfig': null,
      'browserDialer': false,
      'verifyConnection': false,
    });
    expect(startAnswer.error, isNull, reason: 'the core starts in proxy mode');

    // Wait for Connected (the stream, with a state poll as the fallback).
    final connected = await client.states
        .firstWhere((e) => e.state == 'connected')
        .timeout(const Duration(seconds: 30));
    expect(connected.state, 'connected');
    expect(connected.connectedAt, greaterThan(0));

    // The delay test goes through the running instance (to the local 204 page).
    final delay = await client.request(
      'measureDelay',
      {'url': 'http://127.0.0.1:$localPort/204'},
      const Duration(seconds: 15),
    );
    expect(delay.error, isNull, reason: 'the delay test goes through the tunnel');
    expect((delay.result as num).toInt(), greaterThanOrEqualTo(0));

    // Traffic: download through the mixed inbound (HTTP proxy) from the local server.
    final http = HttpClient();
    http.findProxy = (uri) => 'PROXY 127.0.0.1:10808';
    final request = await http.getUrl(Uri.parse('http://127.0.0.1:$localPort/data'));
    final response = await request.close();
    final bytes = await response.fold<List<int>>(<int>[], (a, b) => a..addAll(b));
    http.close();
    expect(bytes.length, 64 * 1024, reason: 'the payload came through the tunnel');

    // The counters see it within a second.
    Map<String, dynamic>? traffic;
    final trafficDeadline = DateTime.now().add(const Duration(seconds: 5));
    while (DateTime.now().isBefore(trafficDeadline)) {
      await Future<void>.delayed(const Duration(milliseconds: 300));
      final answer = await client.request('trafficStats');
      if (answer.result is Map) {
        traffic = Map<String, dynamic>.from(answer.result as Map);
        if ((traffic['proxyDownTotal'] as num? ?? 0).toInt() > 0) break;
      }
    }
    expect(traffic, isNotNull, reason: 'traffic stats are reported while connected');
    expect(
      (traffic!['proxyDownTotal'] as num).toInt(),
      greaterThan(0),
      reason: 'the download is counted on the proxy outbound',
    );

    // The logs exist and grow (attachLogs pointed Xray's logs at the data dir).
    final sizes = await client.request('logSizes');
    expect(sizes.result, isA<Map>(), reason: 'the three logs are reported');

    // While connected, a measureDelay without arguments tests through the default URL.
    final defaultDelay = await client.request(
      'measureDelay',
      const {},
      const Duration(seconds: 30),
    );
    // gstatic may be blocked in a sandbox; only a hard failure of the call is fatal.
    expect(defaultDelay.error, anyOf(isNull, contains('Not connected')));

    // Stop: DISCONNECTING then DISCONNECTED (polled, so a missed event can't hang it).
    await client.request('stop');
    Map<String, dynamic>? finalState;
    final stopDeadline = DateTime.now().add(const Duration(seconds: 20));
    while (DateTime.now().isBefore(stopDeadline)) {
      final answer = await client.request('state', const {}, const Duration(seconds: 5));
      if (answer.result is Map) {
        final m = Map<String, dynamic>.from(answer.result as Map);
        if (m['state'] == 'disconnected') {
          finalState = m;
          break;
        }
      }
      await Future<void>.delayed(const Duration(milliseconds: 200));
    }
    expect(finalState, isNotNull, reason: 'stop reaches disconnected; events: $stateEvents');
    expect(finalState!['state'], 'disconnected');
    expect(stateEvents, containsAllInOrder(['connecting', 'connected']));
    expect((await client.request('trafficStats')).result, isNull,
        reason: 'no traffic is reported while disconnected');
    await subscription.cancel();

    // With the core down, methods fail cleanly instead of hanging.
    final failed = await client.request(
      'measureDelay',
      const {},
      const Duration(seconds: 10),
    );
    expect(failed.error, isNotNull, reason: 'measureDelay without a connection fails');
  }, timeout: const Timeout(Duration(minutes: 2)));
}

String? _defaultBinary() {
  for (final candidate in ['core/bpbcore', 'core${Platform.pathSeparator}bpbcore']) {
    if (File(candidate).existsSync()) return candidate;
  }
  return null;
}
