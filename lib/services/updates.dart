import 'dart:convert';

import 'package:http/http.dart' as http;

/// A newer release on GitHub (see [UpdateChecker]).
class AppUpdate {
  final String version;

  /// The release notes (Markdown, shown as text).
  final String notes;

  /// The release page.
  final String pageUrl;

  /// The APK for this phone's ABI, or null if the release has none (then [pageUrl]).
  final String? apkUrl;

  const AppUpdate({required this.version, required this.notes, required this.pageUrl, this.apkUrl});
}

class UpdateException implements Exception {
  final String message;
  const UpdateException(this.message);

  @override
  String toString() => message;
}

/// Checks the repository's latest release (drafts and pre-releases aren't "latest") against
/// the installed version. Releases are tagged vX.Y.Z and carry one APK per ABI, named
/// `bpb-X.Y.Z-<abi>.apk` (see .github/workflows/release.yml). The Windows build checks its
/// own repository's releases, whose assets are named `bpb-<version>-<abi>.zip` / `.exe`.
class UpdateChecker {
  static const repository = 'bia-pain-bache/BPB-VPN';
  /// The Windows fork, where the Windows releases live.
  static const windowsRepository = 'faripoorasl/BPB-VPN-Windows';

  final http.Client client;
  const UpdateChecker(this.client);

  /// The newer release, or null when [installed] is the latest. [abis]: the device's, preferred
  /// first; a `windows` ABI switches to the Windows repository and asset names.
  Future<AppUpdate?> check({required String installed, required List<String> abis}) async {
    final isWindows = abis.any((a) => a.startsWith('windows'));
    final repo = isWindows ? windowsRepository : repository;
    final latest = 'https://api.github.com/repos/$repo/releases/latest';
    final http.Response res;
    try {
      res = await client
          .get(Uri.parse(latest), headers: {'Accept': 'application/vnd.github+json'})
          .timeout(const Duration(seconds: 20));
    } catch (e) {
      throw UpdateException("Can't reach GitHub: $e");
    }
    if (res.statusCode == 404) {
      throw const UpdateException(
        'GitHub returned 404. The repository may be private or have no published release; update status is unknown.',
      );
    }
    if (res.statusCode != 200) {
      throw UpdateException('GitHub answered HTTP ${res.statusCode}');
    }
    final Map<String, dynamic> json;
    try {
      json = jsonDecode(res.body) as Map<String, dynamic>;
    } catch (_) {
      throw const UpdateException('Unexpected answer from GitHub');
    }
    final version = '${json['tag_name'] ?? ''}'.replaceFirst(RegExp('^v'), '');
    if (compareVersions(version, installed) <= 0) return null;
    final assets = (json['assets'] as List? ?? const []).whereType<Map>();
    String? apkUrl;
    for (final abi in abis) {
      apkUrl = assets
          .where((a) {
            final name = '${a['name']}';
            if (isWindows) {
              return name.endsWith('-$abi.zip') || name.endsWith('-$abi.exe');
            }
            return name.endsWith('-$abi.apk');
          })
          .map((a) => '${a['browser_download_url']}')
          .firstOrNull;
      if (apkUrl != null) break;
    }
    return AppUpdate(
      version: version,
      notes: '${json['body'] ?? ''}'.trim(),
      pageUrl: '${json['html_url'] ?? 'https://github.com/$repo/releases'}',
      apkUrl: apkUrl,
    );
  }
}

/// Compares "X.Y.Z" versions numerically (missing or non-numeric parts count as 0); anything
/// after a "+" or "-" (build number, pre-release) is ignored.
int compareVersions(String a, String b) {
  List<int> parts(String v) => v.split(RegExp(r'[+-]')).first.split('.').map((p) => int.tryParse(p) ?? 0).toList();
  final pa = parts(a), pb = parts(b);
  for (var i = 0; i < 3; i++) {
    final x = i < pa.length ? pa[i] : 0, y = i < pb.length ? pb[i] : 0;
    if (x != y) return x.compareTo(y);
  }
  return 0;
}
