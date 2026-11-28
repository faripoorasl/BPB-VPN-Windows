import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

/// A JSON-lines request/response pair.
class _PendingRequest {
  final Completer<CoreResponse> completer = Completer<CoreResponse>();
  final int id;
  _PendingRequest(this.id);
}

/// The bpbcore host's answers: result or error message.
class CoreResponse {
  final Object? result;
  final String? error;
  const CoreResponse(this.result, this.error);
}

/// One state event from the host, the app's VpnStateEvent fields.
class CoreStateEvent {
  final String state;
  final String? error;
  final int connectedAt;
  final String? detail;
  const CoreStateEvent({
    required this.state,
    this.error,
    required this.connectedAt,
    this.detail,
  });

  factory CoreStateEvent.fromJson(Map<String, dynamic> json) => CoreStateEvent(
    state: json['state'] as String? ?? 'disconnected',
    error: json['error'] as String?,
    connectedAt: (json['connectedAt'] as num?)?.toInt() ?? 0,
    detail: json['detail'] as String?,
  );
}

/// The client of the bpbcore service (see core/cmd/bpbcore): it finds a running service
/// through the info file (or starts one), talks to it over localhost JSON lines and exposes
/// its request methods and its state / scanner event streams.
///
/// The service outlives the app (like Android's foreground VPN service): closing the app
/// leaves the connection up, and the next start reattaches to the same service.
class BpbCoreClient {
  BpbCoreClient._();

  static final BpbCoreClient instance = BpbCoreClient._();

  Socket? _socket;
  int _nextId = 1;
  final Map<int, _PendingRequest> _pending = {};
  final _states = StreamController<CoreStateEvent>.broadcast();
  final _scanner = StreamController<String>.broadcast();
  final _buffer = BytesBuilder(copy: false);

  /// The service's state events.
  Stream<CoreStateEvent> get states => _states.stream;

  /// The scanner's events, each a JSON string (the Android EventChannel's payloads).
  Stream<String> get scanner => _scanner.stream;

  String? _dataDir;
  bool? _elevated;

  /// Where the service keeps its files (logs, tor/, geo assets).
  String? get dataDir => _dataDir;

  /// Whether the connected service has administrator rights.
  bool? get elevated => _elevated;

  /// The rendezvous file, where the running service publishes its port and token.
  /// Overridable with BPBVPN_INFO_FILE or [infoFileOverride] (the tests use it).
  static String? infoFileOverride;
  File get infoFile {
    if (infoFileOverride != null) return File(infoFileOverride!);
    final env = Platform.environment;
    if (env['BPBVPN_INFO_FILE'] case final path? when path.isNotEmpty) {
      return File(path);
    }
    if (Platform.isWindows) {
      final root = env['LOCALAPPDATA'] ?? r'C:\BPBVPN';
      return File(r'$root\BPBVPN\core\core-info.json'
          .replaceFirst(r'$root', root));
    }
    final config = env['XDG_CONFIG_HOME'] ??
        (env['HOME'] == null ? '/tmp/.config' : '${env['HOME']}/.config');
    return File('$config/BPBVPN/core/core-info.json');
  }

  /// Whether a service is reachable right now (without starting one).
  Future<bool> pingService() async {
    try {
      return await _connectToRunning();
    } catch (_) {
      return false;
    }
  }

  /// Connects to the running service or starts one. [elevated]: whether the service needs
  /// administrator rights now (VPN mode) — the service is relaunched with a UAC prompt when
  /// it is running unelevated. [infoFile]: an explicit rendezvous file (the tests use it).
  Future<void> connect({bool elevated = false, String? infoFile}) async {
    if (infoFile != null) infoFileOverride = infoFile;
    if (_socket != null) {
      final info = await hostInfo();
      if (info != null) {
        if (!elevated || info['elevated'] == true) return;
        // VPN mode needs an elevated service: the non-elevated one can't create the TUN.
        await _relaunchElevated();
        return;
      }
      await disconnectSocket();
    }
    if (await _connectToRunning()) {
      final info = await hostInfo();
      if (info != null && (!elevated || info['elevated'] == true)) return;
      await disconnectSocket();
    }
    // No usable service: start one.
    await _startService(elevated: elevated);
    if (!await _connectToRunning()) {
      throw StateError(
        'Could not start the BPB core service. Try starting the app again.',
      );
    }
    final info = await hostInfo();
    if (info != null && elevated && info['elevated'] != true) {
      throw StateError(
        'Administrator rights were not granted. VPN mode needs them; '
        'Proxy mode works without.',
      );
    }
  }

  /// Launches the service binary elevated (a UAC prompt), replacing the current one.
  Future<void> _relaunchElevated() async {
    await stop();
    await disconnectSocket();
    await _startService(elevated: true);
    if (!await _connectToRunning()) {
      throw StateError('The BPB core service could not be started elevated.');
    }
    final host = await hostInfo();
    if (host != null && host['elevated'] != true) {
      throw StateError(
        'Administrator rights were not granted. VPN mode needs them; '
        'Proxy mode works without.',
      );
    }
  }

  /// The service binary, next to the app's own executable (or in the working directory for
  /// the tests). [serviceBinaryOverride] replaces it (the tests use it).
  static String? serviceBinaryOverride;
  static String? serviceBinary() {
    if (serviceBinaryOverride != null) return serviceBinaryOverride;
    final name = Platform.isWindows ? 'bpbcore.exe' : 'bpbcore';
    final exe = File(Platform.resolvedExecutable);
    final directories = [
      exe.parent.path,
      if (exe.parent.path.contains('flutter')) exe.parent.parent.path,
      Directory.current.path,
    ];
    for (final dir in directories) {
      if (dir.isEmpty) continue;
      final candidate = File(
        '$dir${Platform.pathSeparator}$name',
      );
      if (candidate.existsSync()) return candidate.path;
    }
    return null;
  }

  /// Starts the service, elevated with a UAC prompt on Windows when [elevated]. The info
  /// file is the rendezvous: the elevated child's stdout is not readable by the app.
  Future<void> _startService({required bool elevated}) async {
    final exe = serviceBinary();
    if (exe == null) {
      throw StateError(
        'The BPB core service binary (bpbcore) was not found next to the app.',
      );
    }
    final info = infoFile;
    if (await info.exists()) {
      try {
        await info.delete();
      } catch (_) {}
    }
    if (Platform.isWindows && elevated) {
      final script = 'Start-Process -FilePath \'$exe\' -ArgumentList \'--service\' '
          '-Verb RunAs -WindowStyle Hidden';
      final result = await Process.run('powershell', [
        '-NoProfile',
        '-NonInteractive',
        '-Command',
        script,
      ]);
      if (result.exitCode != 0) {
        throw StateError(
          'Administrator rights were not granted. VPN mode needs them; '
          'Proxy mode works without.',
        );
      }
    } else {
      final process = await _spawn(exe);
      // Whatever the service prints (a crash reason lives here) — keep the last few KB.
      final output = StringBuffer();
      void note(String chunk) {
        output.write(chunk);
        final text = output.toString();
        if (text.length > 4096) {
          output
            ..clear()
            ..write(text.substring(text.length - 4096));
        }
      }

      Future<void> drain(Stream<List<int>> stream) async {
        await for (final chunk in stream) {
          note(utf8.decode(chunk, allowMalformed: true));
        }
      }

      unawaited(drain(process.stderr));
      unawaited(drain(process.stdout));
      // The service writes the info file once it is listening; when it dies first, its
      // output says why (an antivirus blocking the binary looks the same at spawn time).
      var exitCode = -1;
      var exited = false;
      unawaited(
        process.exitCode.then((code) {
          exitCode = code;
          exited = true;
        }),
      );
      final deadline = DateTime.now().add(const Duration(seconds: 30));
      while (DateTime.now().isBefore(deadline)) {
        if (await info.exists() && (await info.length()) > 2) return;
        if (exited) {
          throw StateError(
            'The BPB core service exited immediately (code $exitCode).'
            '${_tail(output)}',
          );
        }
        await Future<void>.delayed(const Duration(milliseconds: 150));
      }
      process.kill();
      throw StateError(
        'The BPB core service did not start within 30 seconds.'
        '${_tail(output)}',
      );
    }
    // Only the elevated Windows path reaches this: the child reports through the info file
    // alone (a UAC prompt can take a while).
    final deadline = DateTime.now().add(const Duration(minutes: 2));
    while (DateTime.now().isBefore(deadline)) {
      if (await info.exists() && (await info.length()) > 2) return;
      await Future<void>.delayed(const Duration(milliseconds: 150));
    }
    throw StateError('The BPB core service did not start.');
  }

  /// Runs [exe] as the service; a refusal (antivirus, policy) becomes a clear error.
  Future<Process> _spawn(String exe) async {
    try {
      return await Process.start(exe, ['--service']);
    } on ProcessException catch (e) {
      throw StateError(
        'The BPB core service binary could not be run (${e.message}). '
        'Antivirus software or policy may be blocking it.',
      );
    }
  }

  /// The captured output as " Output: …", or nothing.
  static String _tail(StringBuffer output) {
    var text = output.toString().trim();
    if (text.isEmpty) return '';
    if (text.length > 400) text = '…${text.substring(text.length - 400)}';
    return ' Output: $text';
  }

  Future<bool> _connectToRunning() async {
    if (_socket != null) return true;
    Map<String, dynamic>? info;
    try {
      if (!await infoFile.exists()) return false;
      info = Map<String, dynamic>.from(
        jsonDecode(await infoFile.readAsString()) as Map,
      );
    } catch (_) {
      return false;
    }
    final port = (info['port'] as num?)?.toInt();
    final token = info['token'] as String?;
    if (port == null || token == null) return false;
    try {
      final socket = await Socket.connect(
        '127.0.0.1',
        port,
        timeout: const Duration(seconds: 3),
      );
      _attach(socket);
      _socket!.write('${jsonEncode({'auth': token})}\n');
      await _socket!.flush();
      final answer = await _request(
        'ping',
        const {},
        timeout: const Duration(seconds: 5),
      );
      if (answer.error != null) {
        await disconnectSocket();
        return false;
      }
      _dataDir = info['dataDir'] as String?;
      return true;
    } catch (_) {
      return false;
    }
  }

  void _attach(Socket socket) {
    // One connection at a time: the previous one (e.g. from a ping) goes away.
    final old = _socket;
    _socket = socket;
    if (old != null && old != socket) old.destroy();
    socket.listen(
      _onData,
      onDone: _onSocketDone,
      onError: (_) => _onSocketDone(),
    );
  }

  void _onData(List<int> chunk) {
    _buffer.add(chunk);
    final bytes = _buffer.toBytes();
    var start = 0;
    while (true) {
      final newline = bytes.indexOf(10, start);
      if (newline < 0) break;
      final line = utf8.decode(
        bytes.sublist(start, newline),
        allowMalformed: true,
      );
      start = newline + 1;
      if (line.trim().isEmpty) continue;
      try {
        final json = jsonDecode(line);
        if (json is Map) _onMessage(Map<String, dynamic>.from(json));
      } catch (_) {}
    }
    if (start > 0) {
      final rest = BytesBuilder();
      rest.add(bytes.sublist(start));
      _buffer.clear();
      _buffer.add(rest.toBytes());
    }
  }

  void _onMessage(Map<String, dynamic> json) {
    if (json['event'] != null) {
      switch (json['event'] as String) {
        case 'state':
          _states.add(CoreStateEvent.fromJson(json));
        case 'scanner':
          final payload = json['payload'];
          if (payload is String) _scanner.add(payload);
      }
      return;
    }
    final id = (json['id'] as num?)?.toInt();
    if (id == null) return;
    final pending = _pending.remove(id);
    if (pending == null) return;
    if (json['error'] is String) {
      pending.completer.complete(CoreResponse(null, json['error'] as String));
    } else {
      pending.completer.complete(CoreResponse(json['result'], null));
    }
  }

  void _onSocketDone() {
    _socket = null;
    for (final pending in _pending.values) {
      if (!pending.completer.isCompleted) {
        pending.completer.complete(
          const CoreResponse(
            null,
            'The BPB core service is not reachable. Restart the app to reconnect.',
          ),
        );
      }
    }
    _pending.clear();
  }

  /// Sends one request, connecting to the service first when needed.
  Future<CoreResponse> request(
    String method, [
    Map<String, dynamic> params = const {},
    Duration? timeout,
  ]) async {
    if (_socket == null) {
      await connect();
    }
    return _request(method, params,
        timeout: timeout ?? const Duration(minutes: 5));
  }

  Future<CoreResponse> _request(
    String method,
    Map<String, dynamic> params, {
    required Duration timeout,
  }) async {
    final socket = _socket;
    if (socket == null) {
      return const CoreResponse(null, 'The BPB core service is not running.');
    }
    final id = _nextId++;
    final pending = _PendingRequest(id);
    _pending[id] = pending;
    final timer = Timer(timeout, () {
      if (_pending.remove(id) != null && !pending.completer.isCompleted) {
        pending.completer.complete(
          const CoreResponse(null, 'The BPB core service did not answer.'),
        );
      }
    });
    socket.write('${jsonEncode({'id': id, 'method': method, 'params': params})}\n');
    await socket.flush();
    try {
      return await pending.completer.future;
    } finally {
      timer.cancel();
    }
  }

  /// Closes the socket without touching the service (it keeps running).
  Future<void> disconnectSocket() async {
    final socket = _socket;
    _socket = null;
    try {
      await socket?.close();
    } catch (_) {}
    _onSocketDone();
  }

  /// Asks the service to disconnect (the VPN goes down; the service stays).
  Future<void> stop() async {
    final answer = await request('stop');
    if (answer.error != null) throw StateError(answer.error!);
  }

  /// The host's info (elevated, wintun, versions), or null when unreachable.
  Future<Map<String, dynamic>?> hostInfo() async {
    final answer = await _request(
      'hostInfo',
      const {},
      timeout: const Duration(seconds: 5),
    );
    if (answer.error != null || answer.result is! Map) return null;
    final m = Map<String, dynamic>.from(answer.result as Map);
    _dataDir = m['dataDir'] as String?;
    _elevated = m['elevated'] == true;
    return m;
  }
}
