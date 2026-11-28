import 'dart:convert';

import 'package:flutter/services.dart' show rootBundle;

import 'cloudflare_client.dart';
import 'vpn_channel.dart';

/// Tor bootstrap for retrying requests the direct routes can't reach (the setup wizard's
/// and Settings' "Retry with Tor"): starts the core's setup Tor with the build's embedded
/// bridges, waits for it to bootstrap, then runs [run]'s body with every Cloudflare request
/// through Tor alone ([CloudflareClient.viaTor]).
class SetupTor {
  SetupTor._();

  /// Bootstrap stalls longer than this: give up, so the user isn't left waiting forever.
  static const _stallTimeout = Duration(minutes: 2);

  /// The bridges the build embedded (assets/tor/bridges.json, written at build time by
  /// scripts/fetch_tor_bridges.sh), or null without them: "Retry with Tor" has nothing to
  /// connect with and stays hidden.
  static Future<List<String>?> embeddedBridges() async {
    try {
      final json = jsonDecode(await rootBundle.loadString('assets/tor/bridges.json')) as Map;
      final bridges = (json['bridges'] as List).whereType<String>().toList();
      return bridges.isEmpty ? null : bridges;
    } catch (_) {
      return null;
    }
  }

  /// Connects Tor, then runs [body] through it, stopping Tor afterwards either way.
  /// [onProgress] receives Tor's bootstrap percentage while connecting.
  static Future<void> run(
    List<String> bridges,
    Future<void> Function() body, {
    void Function(int progress)? onProgress,
  }) async {
    final port = await VpnChannel.startSetupTor(bridges);
    try {
      var last = -1;
      var since = DateTime.now();
      while (true) {
        await Future<void>.delayed(const Duration(seconds: 1));
        final status = await VpnChannel.setupTorStatus();
        if (!status.running) throw StateError(status.error ?? 'Tor stopped');
        if (status.progress != last) {
          last = status.progress;
          since = DateTime.now();
        }
        onProgress?.call(status.progress);
        if (status.progress >= 100) break;
        if (DateTime.now().difference(since) > _stallTimeout) {
          throw StateError("Tor couldn't connect with the built-in bridges");
        }
      }
      await CloudflareClient.viaTor(port, body);
    } finally {
      await VpnChannel.stopSetupTor();
    }
  }
}
