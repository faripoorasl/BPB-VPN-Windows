import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:file_selector/file_selector.dart';
import 'package:flutter/services.dart' show Uint8List;

import '../mitm_ca.dart';
import '../my_ip.dart' show countryFlag;
import '../vpn_channel.dart';
import 'bpbcore_client.dart';

/// The Windows implementation of the app's platform channel (vpn_channel.dart): every
/// Android-side native method, backed by the bpbcore service (see core/cmd/bpbcore) and
/// Windows' own file dialogs and Explorer. The protocol surface matches MainActivity.kt.
class WindowsVpnChannel {
  WindowsVpnChannel._();

  static final BpbCoreClient _core = BpbCoreClient.instance;

  /// The latest state event, kept so `states` can also work after a restart.
  static CoreStateEvent? _lastState;

  /// The app connects to (or starts) the service; VPN mode needs the elevated one.
  static Future<bool> prepare({bool vpn = true}) async {
    try {
      await _core.connect(elevated: vpn);
      return true;
    } catch (e) {
      // The message carries the detail (denied UAC, missing binary, ...); the app shows
      // it as the connection error, like Android's denied VPN permission.
      _lastState = CoreStateEvent(
        state: 'disconnected',
        error: e is StateError ? e.message : '$e',
        connectedAt: 0,
      );
      _statesController.add(_lastState!);
      return false;
    }
  }

  static final _statesController =
      StreamController<CoreStateEvent>.broadcast();
  static StreamSubscription<CoreStateEvent>? _subscription;

  /// The service's state events.
  static Stream<VpnStateEvent> get states {
    // (Re)attach to the service's stream; the last event is replayed first, like the
    // Android EventChannel's onListen.
    _subscription ??= _core.states.listen((event) {
      _lastState = event;
      _statesController.add(event);
    });
    return _statesController.stream.map(_toVpnState);
  }

  static VpnStateEvent _toVpnState(CoreStateEvent event) {
    return VpnStateEvent(
      _statusOf(event.state),
      event.error,
      event.connectedAt > 0
          ? DateTime.fromMillisecondsSinceEpoch(event.connectedAt)
          : null,
      event.detail,
    );
  }

  static VpnStatus _statusOf(String state) => VpnStatus.values.firstWhere(
    (s) => s.name == state,
    orElse: () => VpnStatus.disconnected,
  );

  static Future<void> start({
    required Map<String, dynamic> config,
    required String remark,
    required String perAppMode,
    required List<String> apps,
    required int mtu,
    bool appendHttpProxy = false,
    bool proxyOnly = false,
    String? exit,
    bool exitDirect = false,
    String exitRegion = '',
    String? torBridge,
    List<String> torCustomBridges = const [],
    Map<String, dynamic>? psiphonConfig,
    String? sshConfig,
    bool browserDialer = false,
    bool verifyConnection = false,
  }) async {
    await _core.connect(elevated: !proxyOnly);
    final answer = await _core.request('start', {
      'config': jsonEncode(config),
      'remark': remark,
      'perAppMode': perAppMode,
      'apps': apps,
      'mtu': mtu,
      'appendHttpProxy': appendHttpProxy,
      'proxyOnly': proxyOnly,
      'exit': exit,
      'exitDirect': exitDirect,
      'exitRegion': exitRegion,
      'torBridge': torBridge,
      'torCustomBridges': torCustomBridges,
      'psiphonConfig':
          psiphonConfig == null ? null : jsonEncode(psiphonConfig),
      'sshConfig': sshConfig,
      'browserDialer': browserDialer,
      'verifyConnection': verifyConnection,
    });
    if (answer.error != null) throw StateError(answer.error!);
  }

  static Future<void> stop() async {
    final answer = await _core.request('stop');
    if (answer.error != null) throw StateError(answer.error!);
  }

  static Future<bool> get supportsAppendHttpProxy async => true;

  /// Tests a DNS server through the Go networking module.
  static Future<({bool ok, double? latency})> checkDns(
    Map<String, dynamic> request,
  ) async {
    final answer = await _core.request(
      'checkDns',
      {'request': jsonEncode(request)},
    );
    if (answer.error != null) throw StateError(answer.error!);
    if (answer.result is! String) {
      throw StateError('DNS test returned no result.');
    }
    final response = Map<String, dynamic>.from(
      jsonDecode(answer.result as String) as Map,
    );
    final ok = response['ok'] == true;
    final latency = response['latency'];
    return (ok: ok, latency: ok && latency is num ? latency.toDouble() : null);
  }

  static Future<String> readLog({String log = 'xray'}) async {
    final answer = await _core.request('readLog', {'log': log});
    return answer.error == null && answer.result is String
        ? answer.result as String
        : '';
  }

  static Future<Map<String, int>> logSizes() async {
    final answer = await _core.request('logSizes');
    if (answer.result is! Map) return const {};
    return Map<String, int>.from(
      (answer.result as Map).map((k, v) => MapEntry('$k', (v as num).toInt())),
    );
  }

  static Future<void> clearLog() async {
    await _core.request('clearLog');
  }

  /// Saves [content] into the user's Downloads folder and reveals it in Explorer —
  /// Windows' equivalent of the Android share sheet.
  static Future<void> shareFile({
    required String name,
    required String content,
    String title = 'Share',
  }) async {
    final saved = await saveFile(name: name, content: content);
    if (saved == null) return;
    await Process.run('explorer.exe', ['/select,', saved]);
  }

  /// Saves [content] with a Save dialog; null when the user cancelled.
  static Future<String?> saveFile({
    required String name,
    required String content,
  }) async {
    final location = await getSaveLocation(suggestedName: name);
    if (location == null) return null;
    final file = File(location.path);
    await file.writeAsString(content);
    return location.path;
  }

  /// Opens an Open dialog; the chosen file's name and text, or null if cancelled.
  static Future<({String name, String content})?> pickFile() async {
    const group = XTypeGroup(
      label: 'JSON and text',
      extensions: ['json', 'txt', 'conf', 'log'],
    );
    final file = await openFile(acceptedTypeGroups: [group]);
    if (file == null) return null;
    final bytes = await file.readAsBytes();
    if (bytes.length > 1024 * 1024) {
      throw StateError('The file is too large');
    }
    return (name: file.name, content: utf8.decode(bytes, allowMalformed: true));
  }

  /// The setup wizard's Tor with [bridges]; returns its HTTP proxy port.
  static Future<int> startSetupTor(List<String> bridges) async {
    await _core.connect();
    final answer = await _core.request('startSetupTor', {'bridges': bridges});
    if (answer.error != null) throw StateError(answer.error!);
    return (answer.result as num?)?.toInt() ?? 0;
  }

  /// The scanner's events, each a JSON string (see scanner_channel.dart).
  static Stream<String> get scannerEvents => _core.scanner;

  static Future<({int progress, bool running, String? error})>
  setupTorStatus() async {
    final answer = await _core.request('setupTorStatus');
    if (answer.result is! Map) {
      return (progress: 0, running: false, error: null);
    }
    final m = Map<String, dynamic>.from(answer.result as Map);
    return (
      progress: (m['progress'] as num?)?.toInt() ?? 0,
      running: m['running'] == true,
      error: m['error'] as String?,
    );
  }

  static Future<void> stopSetupTor() async {
    await _core.request('stopSetupTor');
  }

  static Future<void> startEchGateway() async {
    final answer = await _core.request('startEchGateway');
    if (answer.error != null) throw StateError(answer.error!);
  }

  static Future<bool> openUrl(String url) async {
    try {
      final answer = await _core.request('openUrl', {'url': url});
      return answer.error == null && answer.result == true;
    } catch (_) {
      return false;
    }
  }

  static Future<bool> mitmCaExists() async {
    try {
      final answer = await _core.request('mitmCaExists');
      return answer.error == null && answer.result == true;
    } catch (_) {
      return false;
    }
  }

  static Future<void> saveMitmCa(MitmCa ca) async {
    final answer = await _core.request(
      'saveMitmCa',
      {'cert': ca.cert, 'key': ca.key},
    );
    if (answer.error != null) throw StateError(answer.error!);
  }

  /// Installs the CA into the user's trusted root store (certutil); returns where the
  /// certificate is, like the Android side returns its Downloads path.
  static Future<String?> installMitmCa() async {
    final answer = await _core.request('installMitmCa');
    if (answer.error != null) throw StateError(answer.error!);
    final dir = _core.dataDir;
    return dir == null ? null : '$dir\\bpb-cert.crt';
  }

  /// Replaces Xray's geo files, each checked against its SHA-256 first.
  static Future<void> saveGeoAssets(
    Map<String, ({List<int> data, String sha256})> files,
  ) async {
    final answer = await _core.request('saveGeoAssets', {
      'files': {
        for (final e in files.entries)
          e.key: {
            'data': base64Encode(e.value.data),
            'sha256': e.value.sha256,
          },
      },
    });
    if (answer.error != null) throw StateError(answer.error!);
  }

  /// The cores' versions, from their own reports.
  static Future<({String xray, String psiphon, String tor})> versions() async {
    try {
      final answer = await _core.request('versions');
      if (answer.result is Map) {
        final m = Map<String, dynamic>.from(answer.result as Map);
        return (
          xray: '${m['xray'] ?? ''}',
          psiphon: '${m['psiphon'] ?? ''}',
          tor: '${m['tor'] ?? ''}',
        );
      }
    } catch (_) {}
    return (xray: '', psiphon: '', tor: '');
  }

  /// The installed app's version and this machine's ABI.
  static Future<({String version, List<String> abis})> appInfo() async {
    try {
      final info = await _core.hostInfo();
      final version = '${info?['hostVersion'] ?? ''}';
      if (version.isNotEmpty) return (version: version, abis: const ['x64']);
    } catch (_) {}
    return (version: '', abis: const ['x64']);
  }

  static Future<int> measureDelay() async {
    final answer = await _core.request(
      'measureDelay',
      const {},
      const Duration(seconds: 20),
    );
    if (answer.error != null) throw StateError(answer.error!);
    return (answer.result as num?)?.toInt() ?? -1;
  }

  static Future<({String ip, String? isp})?> getMyIp(String url) async {
    final answer = await _core.request('getMyIp', {
      'url': url,
    }, const Duration(seconds: 20));
    if (answer.error != null || answer.result is! Map) return null;
    final geo = Map<String, dynamic>.from(answer.result as Map);
    final flag = countryFlag(geo['country_code'] as String);
    return (
      ip: '$flag ${geo['ip'] as String}',
      isp: geo['organization_name'] as String?,
    );
  }

  static Future<String> sshHostKey() async {
    final answer = await _core.request('sshHostKey');
    return answer.error == null && answer.result is String
        ? answer.result as String
        : '';
  }

  /// Proxied traffic: bytes in the last second and since connecting.
  static Future<({int up, int down, int totalUp, int totalDown})>
  traffic() async {
    try {
      final answer = await _core.request('trafficStats');
      if (answer.result is Map) {
        final m = Map<String, dynamic>.from(answer.result as Map);
        return (
          up: (m['proxyUp'] as num?)?.toInt() ?? 0,
          down: (m['proxyDown'] as num?)?.toInt() ?? 0,
          totalUp: (m['proxyUpTotal'] as num?)?.toInt() ?? 0,
          totalDown: (m['proxyDownTotal'] as num?)?.toInt() ?? 0,
        );
      }
    } catch (_) {}
    return (up: 0, down: 0, totalUp: 0, totalDown: 0);
  }

  static Future<List<InstalledApp>> installedApps() async {
    final answer = await _core.request(
      'installedApps',
      const {},
      const Duration(seconds: 30),
    );
    if (answer.error != null || answer.result is! List) return const [];
    return (answer.result as List)
        .whereType<Map>()
        .map(
          (m) => InstalledApp(
            m['package'] as String? ?? '',
            m['label'] as String? ?? '',
            m['system'] as bool? ?? false,
            m['icon'] == null
                ? null
                : Uint8List.fromList((m['icon'] as List).cast<int>()),
          ),
        )
        .toList();
  }

  /// Decodes the QR code in an image file (the Windows replacement for the camera scan);
  /// null when the picture holds no QR code.
  static Future<String?> decodeQrImage(List<int> bytes) async {
    final answer = await _core.request('decodeQr', {
      'image': base64Encode(bytes),
    }, const Duration(seconds: 15));
    if (answer.error != null) return null;
    return answer.result is Map
        ? (answer.result as Map)['text'] as String?
        : null;
  }

  /// The scanner bridge: [options] as JSON, an error message or "" (see scanner_channel.dart).
  static Future<String?> scannerStart(Map<String, dynamic> options) async {
    await _core.connect();
    final answer = await _core.request(
      'scannerStart',
      {'config': jsonEncode(options)},
      const Duration(minutes: 2),
    );
    return answer.error;
  }

  /// geoip:cloudflare from the geoip.dat Xray routes with, one CIDR per line.
  static Future<String> scannerCidrs() async {
    await _core.connect();
    final answer = await _core.request('scannerCidrs');
    if (answer.error != null) throw StateError(answer.error!);
    return answer.result is String ? answer.result as String : '';
  }

  /// scannerPause / scannerResume / scannerStop: an error message or "".
  static Future<String?> scannerCall(String method) async {
    final answer = await _core.request(method);
    return answer.error;
  }

  /// Tells the app's exit that the window is going away and the VPN should keep running
  /// (or shut down when [stopVpn] and it is up). Used by the window's close handler.
  static Future<void> appExiting({required bool stopVpn}) async {
    try {
      if (stopVpn) {
        await _core.request('stop');
        await _core.request('shutdown');
      } else {
        await _core.disconnectSocket();
      }
    } catch (_) {}
  }
}
