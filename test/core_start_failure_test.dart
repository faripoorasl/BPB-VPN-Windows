import 'dart:io';

import 'package:bpb/services/windows/bpbcore_client.dart';
import 'package:flutter_test/flutter_test.dart';

/// When the core service dies at spawn (or is blocked), the start error carries its real
/// output and exit code instead of a generic "did not start" after a full timeout.
void main() {
  test('a service that exits immediately reports why, fast', () async {
    final sandbox = await Directory.systemTemp.createTemp('core_start');
    final script = File('${sandbox.path}${Platform.pathSeparator}bpbcore');
    script.writeAsStringSync('#!/bin/sh\necho "boom: blocked" >&2\nexit 3\n');
    await Process.run('chmod', ['+x', script.path]);
    BpbCoreClient.serviceBinaryOverride = script.path;
    BpbCoreClient.infoFileOverride =
        '${sandbox.path}${Platform.pathSeparator}core-info.json';
    addTearDown(() async {
      BpbCoreClient.serviceBinaryOverride = null;
      BpbCoreClient.infoFileOverride = null;
      await sandbox.delete(recursive: true);
    });

    await expectLater(
      BpbCoreClient.instance.connect(),
      throwsA(
        isA<StateError>().having(
          (e) => e.message,
          'message',
          allOf(contains('code 3'), contains('boom: blocked')),
        ),
      ),
    );
  });
}
