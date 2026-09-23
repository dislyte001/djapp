import 'dart:io';

import 'package:path/path.dart' as path;
import 'package:path_provider_platform_interface/path_provider_platform_interface.dart';
import 'package:path_provider_windows/path_provider_windows.dart';
import 'package:shared_preferences_platform_interface/shared_preferences_platform_interface.dart';
import 'package:shared_preferences_windows/shared_preferences_windows.dart';

Future<void> configurePortableStorage() async {
  if (!Platform.isWindows) return;
  final home = Platform.environment['ZJG_PORTABLE_HOME'];
  if (home == null || home.isEmpty) return;
  if (!path.isAbsolute(home)) {
    throw StateError('便携数据目录无效');
  }
  final root = path.normalize(home);
  final data = path.join(root, 'data');
  final temporary = path.join(root, 'tmp');
  await Directory(data).create(recursive: true);
  await Directory(temporary).create(recursive: true);
  final provider = PortablePathProvider(data, temporary);
  PathProviderPlatform.instance = provider;
  final preferences = SharedPreferencesWindows();
  // ignore: invalid_use_of_visible_for_testing_member
  preferences.pathProvider = provider;
  SharedPreferencesStorePlatform.instance = preferences;
}

class PortablePathProvider extends PathProviderWindows {
  PortablePathProvider(this.data, this.temporary);

  final String data;
  final String temporary;

  @override
  Future<String?> getApplicationSupportPath() async => data;

  @override
  Future<String?> getApplicationCachePath() async {
    final cache = Directory(path.join(data, 'platform-cache'));
    await cache.create(recursive: true);
    return cache.path;
  }

  @override
  Future<String?> getTemporaryPath() async => temporary;
}
