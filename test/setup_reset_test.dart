import 'dart:convert';

import 'package:bpb/models/cf_account.dart';
import 'package:bpb/models/settings.dart';
import 'package:bpb/state/app_state.dart';
import 'package:flutter/services.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

/// "Run setup again": the wizard's data goes away (accounts, WARP / MASQUE registrations,
/// the finished-setup marks) and the wizard comes back, while ordinary settings survive
/// unless asked otherwise.
void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  Future<AppState> seededState(WidgetTester tester, Map<String, Object?> profile) async {
    SharedPreferences.setMockInitialValues({
      'profile': jsonEncode(profile),
      'device': jsonEncode({'setupDone': true}),
    });
    FlutterSecureStorage.setMockInitialValues({});
    final messenger = tester.binding.defaultBinaryMessenger;
    messenger.setMockMethodCallHandler(
      const MethodChannel('bpb/state'),
      (_) async => null,
    );
    addTearDown(
      () => messenger.setMockMethodCallHandler(const MethodChannel('bpb/state'), null),
    );
    final state = AppState();
    await state.init();
    return state;
  }

  testWidgets('the wizard data goes and the wizard comes back, the rest of the settings stay',
      (tester) async {
    final state = await seededState(tester, {
      'general': {'localDns': '9.9.9.9', 'remoteDns': '8.8.8.8'},
      'warp': {
        'account': {
          'privateKey': 'k',
          'publicKey': 'p',
          'ipv6': '2606:4700::1/128',
          'reserved': [1, 2, 3],
        },
        'endpoints': ['162.159.192.1:2408'],
      },
      'masque': {
        'account': {
          'privateKey': 'k',
          'publicKey': 'p',
          'ipv4': '172.16.0.2',
          'ipv6': '2606:4700::2',
        },
        'endpoints': ['162.159.198.1:443'],
      },
    });
    // A saved account with both deployments (its workers are deleted from Cloudflare too,
    // which fails here without real credentials and falls back to forgetting it locally).
    state.cfAccounts = const [
      SavedCfAccount(
        id: 'a',
        name: 'Mine',
        token: 'token',
        email: 'me@example.com',
        uuid: 'u',
        deployments: [
          Deployment(type: WorkerType.workers, name: 'wk', host: 'wk.example.com', version: 1),
          Deployment(type: WorkerType.pages, name: 'pg', host: 'pg.example.com', version: 1),
        ],
      ),
    ];

    // init() starts periodic timers; dispose inside the body, before the framework's
    // pending-timer check (which older Flutter runs before addTearDown).
    try {
      expect(state.needsSetup, isFalse, reason: 'setup was finished');
      expect(state.profile.warp.account, isNotNull);
      expect(state.profile.masque.account, isNotNull);
      expect(state.cfAccounts, hasLength(1));

      await state.runSetupAgain();

      expect(state.cfAccounts, isEmpty, reason: 'accounts are forgotten');
      expect(state.profile.warp.account, isNull, reason: 'WARP registration goes');
      expect(state.profile.masque.account, isNull, reason: 'MASQUE registration goes');
      expect(state.device.setupDone, isFalse);
      expect(state.needsSetup, isTrue, reason: 'the wizard replaces the home page');
      // Endpoints survive (they belong to the network, not the account), like resetSettings.
      expect(state.profile.warp.endpoints, ['162.159.192.1:2408']);
      expect(state.profile.masque.endpoints, ['162.159.198.1:443']);
      // Ordinary settings survive.
      expect(state.profile.general.localDns, '9.9.9.9');
      expect(state.profile.general.remoteDns, '8.8.8.8');
    } finally {
      state.dispose();
    }
  });

  testWidgets('with resetAllSettings the rest of the settings return to their defaults too',
      (tester) async {
    final state = await seededState(tester, {
      'general': {'localDns': '9.9.9.9', 'remoteDns': '8.8.8.8'},
      'warp': {
        'account': {
          'privateKey': 'k',
          'publicKey': 'p',
          'ipv6': '2606:4700::1/128',
          'reserved': [1, 2, 3],
        },
        'endpoints': ['162.159.192.1:2408'],
      },
    });

    try {
      await state.runSetupAgain(resetAllSettings: true);

      expect(state.profile.general.remoteDns, isNot('8.8.8.8'), reason: 'back to the default');
      expect(state.profile.warp.account, isNull);
      expect(state.needsSetup, isTrue);
    } finally {
      state.dispose();
    }
  });
}
