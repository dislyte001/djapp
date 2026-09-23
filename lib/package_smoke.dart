import 'dart:convert';
import 'dart:io';

import 'package:media_kit/media_kit.dart';
import 'package:path/path.dart' as path;
import 'package:path_provider/path_provider.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'core_bridge.dart';
import 'media_pipeline.dart';

Future<void> runPackageSmoke(List<String> arguments) async {
  if (arguments.length != 3) exit(2);
  final report = File(arguments[1]), input = arguments[2];
  Player? player;
  try {
    final repository = NativeRepository();
    await repository.initialize();
    final portableHome = Platform.environment['ZJG_PORTABLE_HOME'];
    if (portableHome != null) {
      final root = path.normalize(portableHome);
      final support = await getApplicationSupportDirectory();
      final cache = await getApplicationCacheDirectory();
      final temporary = await getTemporaryDirectory();
      if (support.path != path.join(root, 'data') ||
          cache.path != path.join(root, 'data', 'platform-cache') ||
          temporary.path != path.join(root, 'tmp')) {
        throw StateError('便携数据路径不正确');
      }
      final preferences = await SharedPreferences.getInstance();
      if (!await preferences.setString('portablePackageSmoke', 'ok') ||
          preferences.getString('portablePackageSmoke') != 'ok' ||
          !await preferences.remove('portablePackageSmoke')) {
        throw StateError('便携偏好设置无法读写');
      }
    }
    final executor = FFmpegExecutor();
    final probe = await executor.probe(input);
    verifyMediaDuration(probe, 3);
    final remuxed = File(
      '${report.parent.path}${Platform.pathSeparator}remuxed.mkv',
    );
    await executor.run([
      '-i',
      input,
      '-map',
      '0:v:0',
      '-c',
      'copy',
      remuxed.path,
    ]);
    verifyMediaDuration(await executor.probe(remuxed.path), 3);
    player = Player(
      configuration: const PlayerConfiguration(muted: true, vo: 'null'),
    );
    await player.setAudioTrack(AudioTrack.no());
    final advancing = player.stream.position.firstWhere(
      (time) => time.inMilliseconds >= 400,
    );
    await player.open(Media(remuxed.path));
    await advancing.timeout(const Duration(seconds: 20));
    await report.writeAsString(
      jsonEncode({
        'ok': true,
        'nativeCore': true,
        'ffprobe': true,
        'remux': true,
        'mediaKitPlayback': true,
      }),
      flush: true,
    );
    await player.dispose();
    exit(0);
  } catch (error) {
    await player?.dispose();
    await report.writeAsString(
      jsonEncode({'ok': false, 'error': error.toString()}),
      flush: true,
    );
    exit(1);
  }
}
