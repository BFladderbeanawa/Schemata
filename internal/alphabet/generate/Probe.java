import java.lang.reflect.Method;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Map;

// Obfuscated names are pinned by Mojang's official 1.20.1 server mappings.
// This independently enumerates the live registry, not the JSON report writer.
public class Probe {
    public static void main(String[] args) throws Exception {
        Class.forName("aa").getMethod("a").invoke(null); // SharedConstants.tryDetectVersion
        Class.forName("acs").getMethod("a").invoke(null); // Bootstrap.bootStrap
        Class<?> registry = Class.forName("hr");
        Method key = registry.getMethod("b", Object.class);
        Object blocks = Class.forName("jb").getField("f").get(null);
        Method definition = Class.forName("cpn").getMethod("l");
        Method possibleStates = Class.forName("dcc").getMethod("a");
        Method values = Class.forName("dcd").getMethod("C");
        // BlockStateBase.isSolidRender(EmptyBlockGetter.INSTANCE, BlockPos.ZERO) and hasBlockEntity.
        Class<?> stateBase = Class.forName("dca$a");
        Method solidRender = stateBase.getMethod("i", Class.forName("cls"), Class.forName("gu"));
        Method hasBlockEntity = stateBase.getMethod("t");
        Object emptyGetter = Class.forName("cmb").getField("a").get(null);
        Object origin = Class.forName("gu").getField("b").get(null);
        List<String> solid = new ArrayList<>();
        Method propertyName = Class.forName("dde").getMethod("f");
        Method propertyValue = Class.forName("dde").getMethod("a", Comparable.class);
        List<String> states = new ArrayList<>();
        for (Object block : (Iterable<?>) blocks) {
            String name = key.invoke(blocks, block).toString();
            for (Object state : (Iterable<?>) possibleStates.invoke(definition.invoke(block))) {
                List<String> properties = new ArrayList<>();
                for (Map.Entry<?, ?> entry : ((Map<?, ?>) values.invoke(state)).entrySet()) {
                    properties.add(propertyName.invoke(entry.getKey()) + "="
                        + propertyValue.invoke(entry.getKey(), entry.getValue()));
                }
                Collections.sort(properties);
                String line = name + "[" + String.join(",", properties) + "]";
                states.add(line);
                if ((boolean) solidRender.invoke(state, emptyGetter, origin)
                    && !(boolean) hasBlockEntity.invoke(state)) {
                    solid.add(line);
                }
            }
        }
        Collections.sort(states);
        Files.write(Path.of(args[0]), states, StandardCharsets.UTF_8);
        Collections.sort(solid);
        Files.write(Path.of(args[2]), solid, StandardCharsets.UTF_8);
        Object patterns = Class.forName("jb").getField("al").get(null);
        Method hash = Class.forName("cze").getMethod("a");
        Method byHash = Class.forName("cze").getMethod("a", String.class);
        Class<?> compound = Class.forName("qr");
        Class<?> list = Class.forName("qx");
        Class<?> dye = Class.forName("cen");
        Method putString = compound.getMethod("a", String.class, String.class);
        Method putInt = compound.getMethod("a", String.class, int.class);
        Method add = list.getMethod("c", int.class, Class.forName("rk"));
        Method createPatterns = Class.forName("czd").getMethod("a", dye, list);
        Method holderValue = Class.forName("he").getMethod("a");
        Object white = dye.getField("a").get(null);
        List<String> nativePatterns = new ArrayList<>();
        for (Object pattern : (Iterable<?>) patterns) {
            String nativeName = (String) hash.invoke(pattern);
            if (byHash.invoke(null, nativeName) == null) {
                throw new IllegalStateException("native pattern rejected: " + nativeName);
            }
            for (int color = 0; color < 16; color++) {
                Object layers = list.getConstructor().newInstance();
                for (int length = 0; length <= 6; length++) {
                    List<?> parsed = (List<?>) createPatterns.invoke(null, white, layers);
                    if (parsed.size() != length + 1) {
                        throw new IllegalStateException("native layers rejected: " + nativeName);
                    }
                    for (int i = 1; i < parsed.size(); i++) {
                        Object pair = parsed.get(i);
                        Object holder = pair.getClass().getMethod("getFirst").invoke(pair);
                        if (holderValue.invoke(holder) != pattern) {
                            throw new IllegalStateException("native pattern changed: " + nativeName);
                        }
                        Object parsedColor = pair.getClass().getMethod("getSecond").invoke(pair);
                        if ((int) dye.getMethod("a").invoke(parsedColor) != color) {
                            throw new IllegalStateException("native dye color rejected: " + color);
                        }
                    }
                    Object layer = compound.getConstructor().newInstance();
                    putString.invoke(layer, "Pattern", nativeName);
                    putInt.invoke(layer, "Color", color);
                    add.invoke(layers, length, layer);
                }
            }
            nativePatterns.add(nativeName + "\t" + key.invoke(patterns, pattern));
        }
        Collections.sort(nativePatterns);
        Files.write(Path.of(args[1]), nativePatterns, StandardCharsets.UTF_8);
        if (byHash.invoke(null, "minecraft:base") != null || byHash.invoke(null, "unknown") != null) {
            throw new IllegalStateException("non-native banner pattern accepted");
        }
    }
}
