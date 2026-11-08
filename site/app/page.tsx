import { Nav } from "@/components/sections/Nav";
import { Hero } from "@/components/sections/Hero";
import { OneKey } from "@/components/sections/OneKey";
import { Pillars } from "@/components/sections/Pillars";
import { Move } from "@/components/sections/Move";
import { Speed } from "@/components/sections/Speed";
import { PickUp } from "@/components/sections/PickUp";
import { Terminal } from "@/components/sections/Terminal";
import { Security } from "@/components/sections/Security";
import { OpenSource } from "@/components/sections/OpenSource";
import { Install } from "@/components/sections/Install";
import { Footer } from "@/components/sections/Footer";

export default function Page() {
  return (
    <>
      <Nav />
      <main>
        <Hero />
        <OneKey />
        <Pillars />
        <Move />
        <Speed />
        <PickUp />
        <Terminal />
        <Security />
        <OpenSource />
        <Install />
      </main>
      <Footer />
    </>
  );
}
