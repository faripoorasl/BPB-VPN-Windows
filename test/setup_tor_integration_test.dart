import 'dart:convert';
import 'dart:io';

import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';

import 'package:bpb/services/windows/bpbcore_client.dart';

/// The "Retry with Tor" core path against the REAL core: spawns bpbcore, starts the setup
/// Tor with the build's embedded bridges (assets/tor/bridges.json) and waits for a real
/// bootstrap through the pluggable transports (obfs4 / snowflake / webtunnel / meek). Needs
/// internet and a few minutes at most. BPBCORE_BIN points at the built binary with a tor/
/// bundle staged next to it (see scripts/test_core.sh); without it the suite skips.
void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  final bin = Platform.environment['BPBCORE_BIN'];
  if (bin == null || !File(bin).existsSync()) {
    test(
      'setup Tor bootstrap (skipped: build the core first, see scripts/test_core.sh)',
      () {},
      skip: 'BPBCORE_BIN is not built',
    );
    return;
  }
  // The core runs the setup Tor from a tor/ dir next to its own binary (binDir); without the
  // staged bundle (CI: tests.yml downloads it) there is nothing to bootstrap with.
  final binDir = File(bin).parent.path;
  if (!File('$binDir${Platform.pathSeparator}tor${Platform.pathSeparator}tor').existsSync() ||
      !File('$binDir${Platform.pathSeparator}tor${Platform.pathSeparator}lyrebird').existsSync()) {
    test(
      'setup Tor bootstrap (skipped: no tor bundle next to BPBCORE_BIN)',
      () {},
      skip: 'the tor bundle is not staged next to BPBCORE_BIN',
    );
    return;
  }

  late Directory sandbox;

  setUpAll(() async {
    sandbox = await Directory.systemTemp.createTemp('bpb_setup_tor');
  });

  tearDownAll(() async {
    try {
      await BpbCoreClient.instance.request(
        'stopSetupTor',
        const {},
        const Duration(seconds: 10),
      );
    } catch (_) {}
    await BpbCoreClient.instance.disconnectSocket();
    await sandbox.delete(recursive: true);
  });

  test('the setup Tor bootstraps with the embedded bridges and exposes its HTTP port',
      () async {
    final json = jsonDecode(await rootBundle.loadString('assets/tor/bridges.json')) as Map;
    final bridges = (json['bridges'] as List).whereType<String>().toList();
    expect(bridges, isNotEmpty, reason: 'the build embeds bridges for Retry with Tor');
    expect(bridges.any((b) => b.startsWith('meek_lite ')), isTrue,
        reason: 'meek-azure is the transport that survives obfs4 port blocking');

    final client = BpbCoreClient.instance;
    final infoFile = File('${sandbox.path}${Platform.pathSeparator}core-info.json');
    final process = await Process.start(bin, ['--service'], environment: {
      'BPBVPN_INFO_FILE': infoFile.path,
      'BPBVPN_DATA_DIR': sandbox.path,
    });
    addTearDown(process.kill);
    process.stderr.drain();
    final deadline = DateTime.now().add(const Duration(seconds: 20));
    while (DateTime.now().isBefore(deadline)) {
      if (await infoFile.exists() && await infoFile.length() > 2) break;
      await Future<void>.delayed(const Duration(milliseconds: 100));
    }
    await client.connect(infoFile: infoFile.path);
    expect(await client.pingService(), isTrue);

    // Start Tor the way SetupTor.run does, then follow progress like the UI does.
    final answer = await client.request(
      'startSetupTor',
      {'bridges': bridges},
      const Duration(seconds: 30),
    );
    expect(answer.error, isNull, reason: answer.error ?? '');
    final port = (answer.result as num).toInt();
    expect(port, greaterThan(0), reason: 'the HTTP tunnel port for Dart\'s client');

    var progress = 0;
    final bootstrapDeadline = DateTime.now().add(const Duration(minutes: 4));
    while (DateTime.now().isBefore(bootstrapDeadline)) {
      await Future<void>.delayed(const Duration(seconds: 2));
      final st = await client.request(
        'setupTorStatus',
        const {},
        const Duration(seconds: 10),
      );
      expect(st.error, isNull, reason: st.error ?? '');
      final m = Map<String, dynamic>.from(st.result as Map);
      progress = (m['progress'] as num?)?.toInt() ?? 0;
      if (m['running'] != true) fail('Tor stopped early: ${m['error']}');
      if (progress >= 100) break;
    }
    expect(progress, 100, reason: 'Tor bootstrapped through the embedded bridges');

    // Stop Tor while the service can still hear us: the teardowns only cover failures,
    // and a Tor nobody stops would hold the setup ports from the next run.
    final stopped = await client.request(
      'stopSetupTor',
      const {},
      const Duration(seconds: 10),
    );
    expect(stopped.error, isNull, reason: stopped.error ?? '');
    // Give tor a moment to exit, then make sure the ports are free again.
    await Future<void>.delayed(const Duration(seconds: 2));
    try {
      final socket = await Socket.connect('127.0.0.1', port, timeout: const Duration(seconds: 2));
      socket.destroy();
      fail('the setup Tor HTTP port is still listening after stopSetupTor');
    } on SocketException {
      // Expected: tor is gone and the port is closed again.
    }
  }, timeout: const Timeout(Duration(minutes: 6)));
}
